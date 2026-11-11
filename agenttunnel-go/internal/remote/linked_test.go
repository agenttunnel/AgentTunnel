package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// fakeLinkedSession is the whatsmeow client boundary with no network. Hooks
// drive the QR and linked callbacks synchronously.
type fakeLinkedSession struct {
	mu          sync.Mutex
	connectHook func(onQR, onLinked func(string), onError func(error)) error
	sent        []string
	sentIDs     []string
	inbound     func(InboundMessage)
	logouts     int
	closes      int
}

func (f *fakeLinkedSession) Connect(_ context.Context, onQR, onLinked func(string), onError func(error)) error {
	f.mu.Lock()
	hook := f.connectHook
	f.mu.Unlock()
	if hook != nil {
		return hook(onQR, onLinked, onError)
	}
	onLinked("Linked device · +15551234567")
	return nil
}

func (f *fakeLinkedSession) Send(_ context.Context, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	id := fmt.Sprintf("sent-%d", len(f.sent))
	f.sentIDs = append(f.sentIDs, id)
	return id, nil
}

func (f *fakeLinkedSession) SetInbound(handler func(InboundMessage)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbound = handler
}

// deliver hands one message to whatever handler the adapter installed.
func (f *fakeLinkedSession) deliver(msg InboundMessage) bool {
	f.mu.Lock()
	handler := f.inbound
	f.mu.Unlock()
	if handler == nil {
		return false
	}
	handler(msg)
	return true
}

func (f *fakeLinkedSession) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logouts++
	return nil
}

func (f *fakeLinkedSession) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
}

func (f *fakeLinkedSession) counts() (logouts, closes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logouts, f.closes
}

// sequentialOpener hands out sessions in order and records how many were
// opened. The last session is repeated, so a test can assert an extra open.
func sequentialOpener(sessions ...*fakeLinkedSession) (linkedOpener, func() int) {
	var mu sync.Mutex
	opened := 0
	open := func(context.Context, string) (linkedSession, error) {
		mu.Lock()
		defer mu.Unlock()
		index := min(opened, len(sessions)-1)
		opened++
		return sessions[index], nil
	}
	return open, func() int {
		mu.Lock()
		defer mu.Unlock()
		return opened
	}
}

// The adapter surfaces QR payloads, reports the linked device, sends to the
// account, and unlinks on Disconnect.
func TestLinkedAdapterPairsSendsAndUnlinks(t *testing.T) {
	ctx := context.Background()
	var qr string
	var link func(string)
	sess := &fakeLinkedSession{connectHook: func(onQR, onLinked func(string), _ func(error)) error {
		onQR("2@qr-payload")
		link = onLinked
		return nil
	}}
	open, opened := sequentialOpener(sess)
	a := newLinkedAdapter(t.TempDir(), open)

	var detail string
	if err := a.Connect(ctx, func(q string) { qr = q }, func(d string) { detail = d }, func(error) {}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if qr != "2@qr-payload" {
		t.Fatalf("qr = %q, want the payload from the channel", qr)
	}
	if link == nil {
		t.Fatal("the session never handed out its linked callback")
	}
	link("Linked device · +15551234567")
	if !strings.Contains(detail, "+15551234567") {
		t.Fatalf("linked detail = %q, want the phone number", detail)
	}

	if err := a.Send(ctx, "turn complete"); err != nil {
		t.Fatalf("send: %v", err)
	}
	sess.mu.Lock()
	sent := append([]string(nil), sess.sent...)
	sess.mu.Unlock()
	if len(sent) != 1 || sent[0] != "turn complete" {
		t.Fatalf("sent = %v", sent)
	}

	if err := a.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	logouts, closes := sess.counts()
	if logouts != 1 || closes != 1 {
		t.Fatalf("after disconnect: logouts %d, closes %d; want 1 and 1", logouts, closes)
	}
	if err := a.Send(ctx, "after disconnect"); err == nil {
		t.Fatal("send after disconnect must fail")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatalf("second disconnect: %v", err)
	}
	if logouts, _ = sess.counts(); logouts != 1 {
		t.Fatalf("second disconnect logged out again: %d", logouts)
	}
	if opened() != 1 {
		t.Fatalf("opened %d sessions, want 1", opened())
	}
}

// A second Connect while the first is still pairing reuses the session.
func TestLinkedAdapterConnectWhilePairingOpensOnce(t *testing.T) {
	sess := &fakeLinkedSession{connectHook: func(onQR, _ func(string), _ func(error)) error {
		onQR("2@qr")
		return nil
	}}
	open, opened := sequentialOpener(sess)
	a := newLinkedAdapter(t.TempDir(), open)

	if err := a.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := a.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {}); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if opened() != 1 {
		t.Fatalf("opened %d sessions, want 1", opened())
	}
}

// A failed connect closes its session so a retry starts clean.
func TestLinkedAdapterConnectFailureClosesSession(t *testing.T) {
	boom := errors.New("dial failed")
	first := &fakeLinkedSession{connectHook: func(func(string), func(string), func(error)) error {
		return boom
	}}
	second := &fakeLinkedSession{}
	open, opened := sequentialOpener(first, second)
	a := newLinkedAdapter(t.TempDir(), open)

	if err := a.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {}); !errors.Is(err, boom) {
		t.Fatalf("connect = %v, want the session error", err)
	}
	if _, closes := first.counts(); closes != 1 {
		t.Fatalf("failed session closes = %d, want 1", closes)
	}
	if err := a.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {}); err != nil {
		t.Fatalf("retry connect: %v", err)
	}
	if opened() != 2 {
		t.Fatalf("opened %d sessions, want a fresh one on retry", opened())
	}
}

// Close stops the socket without unlinking: a daemon restart must find the
// saved session.
func TestLinkedAdapterCloseKeepsDevice(t *testing.T) {
	sess := &fakeLinkedSession{}
	open, _ := sequentialOpener(sess)
	a := newLinkedAdapter(t.TempDir(), open)
	if err := a.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	a.Close()
	logouts, closes := sess.counts()
	if logouts != 0 || closes != 1 {
		t.Fatalf("after close: logouts %d, closes %d; want 0 and 1", logouts, closes)
	}
}

// A Disconnect that lands while the store is still opening must not leave a
// live session behind: the next Connect has to start from a clean state.
func TestLinkedAdapterDisconnectWhileOpeningDropsSession(t *testing.T) {
	sess := &fakeLinkedSession{}
	opening := make(chan struct{})
	release := make(chan struct{})
	open := func(context.Context, string) (linkedSession, error) {
		close(opening)
		<-release
		return sess, nil
	}
	a := newLinkedAdapter(t.TempDir(), open)

	done := make(chan error, 1)
	go func() {
		done <- a.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {})
	}()
	<-opening
	if err := a.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, closes := sess.counts(); closes != 1 {
		t.Fatalf("the abandoned session closes = %d, want 1", closes)
	}
	if err := a.Send(context.Background(), "should not send"); err == nil {
		t.Fatal("a disconnected adapter must not keep a session")
	}
}

// The real store opens with the pure-Go SQLite driver and leaves an unpaired
// device waiting for a QR. No network is touched.
func TestWhatsmeowSessionOpensPureGoStore(t *testing.T) {
	dir := t.TempDir()
	sess, err := openWhatsmeowSession(context.Background(), dir)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer sess.Close()
	info, err := os.Stat(filepath.Join(dir, "whatsapp.db"))
	if err != nil {
		t.Fatalf("session database: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("session database mode = %o, want 600", perm)
	}
	linked, ok := sess.(*whatsmeowSession)
	if !ok {
		t.Fatalf("session = %T, want *whatsmeowSession", sess)
	}
	if linked.client.Store.ID != nil {
		t.Fatalf("a fresh store must start unpaired, got %v", linked.client.Store.ID)
	}
}

// A session failure that arrives after Connect returned (an expired QR, the
// phone unlinking the device) must drop the adapter's session so the next
// Connect starts a fresh one, and must reach the caller.
func TestLinkedAdapterAsyncErrorDropsSession(t *testing.T) {
	ctx := context.Background()
	var fail func(error)
	first := &fakeLinkedSession{connectHook: func(onQR, _ func(string), onError func(error)) error {
		fail = onError
		onQR("2@qr")
		return nil
	}}
	second := &fakeLinkedSession{}
	open, opened := sequentialOpener(first, second)
	a := newLinkedAdapter(t.TempDir(), open)

	var got error
	if err := a.Connect(ctx, func(string) {}, func(string) {}, func(err error) { got = err }); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if fail == nil {
		t.Fatal("the session never handed out its error callback")
	}
	boom := errors.New("the pairing QR expired")
	fail(boom)
	if !errors.Is(got, boom) {
		t.Fatalf("async error = %v, want the session error", got)
	}
	if _, closes := first.counts(); closes != 1 {
		t.Fatalf("failed session closes = %d, want 1", closes)
	}
	if err := a.Send(ctx, "should not send"); err == nil {
		t.Fatal("a dropped session must not send")
	}
	if err := a.Connect(ctx, func(string) {}, func(string) {}, func(error) {}); err != nil {
		t.Fatalf("retry connect: %v", err)
	}
	if opened() != 2 {
		t.Fatalf("opened %d sessions, want a fresh one after the failure", opened())
	}
}

// A Close that lands while the store is still opening must not leave a live
// session behind, exactly like Disconnect.
func TestLinkedAdapterCloseWhileOpeningDropsSession(t *testing.T) {
	sess := &fakeLinkedSession{}
	opening := make(chan struct{})
	release := make(chan struct{})
	open := func(context.Context, string) (linkedSession, error) {
		close(opening)
		<-release
		return sess, nil
	}
	a := newLinkedAdapter(t.TempDir(), open)

	done := make(chan error, 1)
	go func() {
		done <- a.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {})
	}()
	<-opening
	a.Close()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, closes := sess.counts(); closes != 1 {
		t.Fatalf("the abandoned session closes = %d, want 1", closes)
	}
	if err := a.Send(context.Background(), "should not send"); err == nil {
		t.Fatal("a closed adapter must not keep a session")
	}
}

// The session store DSN must never put the path in the URI authority: a
// relative dir and a Windows path both broke modernc.org/sqlite that way.
func TestSQLiteDSN(t *testing.T) {
	const query = "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	cases := []struct{ name, path, want string }{
		{"relative", "state/whatsapp.db", "file:state/whatsapp.db" + query},
		{"windows", `C:\Users\x\whatsapp.db`, "file:C:/Users/x/whatsapp.db" + query},
		{"absolute", "/var/lib/agenttunnel/whatsapp.db", "file:/var/lib/agenttunnel/whatsapp.db" + query},
	}
	for _, tc := range cases {
		got := sqliteDSN(tc.path)
		if got != tc.want {
			t.Errorf("%s: sqliteDSN(%q) = %q, want %q", tc.name, tc.path, got, tc.want)
		}
		if strings.Contains(got, "file://") {
			t.Errorf("%s: DSN %q has a URI authority", tc.name, got)
		}
	}
}

// The real store must open from a relative state dir; releases let --home-dir
// be relative on every OS.
func TestWhatsmeowSessionOpensFromRelativeStateDir(t *testing.T) {
	t.Chdir(t.TempDir())
	sess, err := openWhatsmeowSession(context.Background(), ".")
	if err != nil {
		t.Fatalf("open session from a relative dir: %v", err)
	}
	defer sess.Close()
	info, err := os.Stat("whatsapp.db")
	if err != nil {
		t.Fatalf("session database: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("session database mode = %o, want 600", perm)
	}
}

// Only the linked account's own chat reaches the handler: another chat, a
// group, and a media-only message are all dropped before the bridge sees them.
func TestInboundTextSelfChatOnly(t *testing.T) {
	own := types.NewJID("15551234567", types.DefaultUserServer)
	other := types.NewJID("15550000000", types.DefaultUserServer)
	lid := types.NewJID("99887766", "lid")
	text := &waE2E.Message{Conversation: proto.String("hello")}
	extended := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("same")}}

	info := func(chat, sender types.JID, fromMe, group bool) types.MessageInfo {
		return types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe, IsGroup: group},
			ID:            "m1",
		}
	}

	cases := []struct {
		name     string
		own      types.JID
		info     types.MessageInfo
		message  *waE2E.Message
		want     string
		ok       bool
		fromSelf bool
	}{
		{name: "self chat", own: own, info: info(own, own, true, false), message: text, want: "hello", ok: true, fromSelf: true},
		{name: "extended text", own: own, info: info(own, own, true, false), message: extended, want: "same", ok: true, fromSelf: true},
		{name: "another chat", own: own, info: info(other, other, false, false), message: text},
		{name: "group", own: own, info: info(own, own, true, true), message: text},
		{name: "no text", own: own, info: info(own, own, true, false), message: &waE2E.Message{}},
		// A self message addressed by LID is still the self chat.
		{name: "lid self chat", own: own, info: info(lid, lid, true, false), message: text, want: "hello", ok: true, fromSelf: true},
		// A message in our own chat that the account did not send is not ours.
		{name: "foreign sender in own chat", own: own, info: info(own, other, false, false), message: text, want: "hello", ok: true},
	}
	for _, tc := range cases {
		msg, ok := inboundText(tc.own, tc.info, tc.message)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if msg.Text != tc.want || msg.ID != "m1" || msg.FromSelf != tc.fromSelf {
			t.Errorf("%s: message = %+v, want text %q fromSelf %v", tc.name, msg, tc.want, tc.fromSelf)
		}
	}
}

// The adapter passes the handler to a session that connects later, and Send
// reports the id so the bridge can drop its echo.
func TestLinkedAdapterForwardsInbound(t *testing.T) {
	sess := &fakeLinkedSession{}
	open, _ := sequentialOpener(sess)
	a := newLinkedAdapter(t.TempDir(), open)

	var got []InboundMessage
	a.SetInbound(func(msg InboundMessage) { got = append(got, msg) })
	if err := a.Connect(context.Background(), func(string) {}, func(string) {}, func(error) {}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !sess.deliver(InboundMessage{ID: "m1", Text: "hi", FromSelf: true}) {
		t.Fatal("the session never received the inbound handler")
	}
	if len(got) != 1 || got[0].ID != "m1" || got[0].Text != "hi" {
		t.Fatalf("delivered = %+v, want the session's message", got)
	}

	id, err := a.SendTracked(context.Background(), "reply")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id != "sent-1" {
		t.Fatalf("send id = %q, want the session's message id", id)
	}
}

// The default adapter is the linked-device one unless the Cloud API credentials
// are configured.
func TestNewDefaultAdapterPrefersLinkedDevice(t *testing.T) {
	t.Setenv("AT_WHATSAPP_TOKEN", "")
	t.Setenv("AT_WHATSAPP_PHONE_NUMBER_ID", "")
	t.Setenv("AT_WHATSAPP_TO", "")
	if _, ok := NewDefaultAdapter(t.TempDir()).(*LinkedAdapter); !ok {
		t.Fatalf("default adapter = %T, want *LinkedAdapter", NewDefaultAdapter(t.TempDir()))
	}

	t.Setenv("AT_WHATSAPP_TOKEN", "tok")
	t.Setenv("AT_WHATSAPP_PHONE_NUMBER_ID", "123")
	t.Setenv("AT_WHATSAPP_TO", "15551234567")
	if _, ok := NewDefaultAdapter(t.TempDir()).(*CloudAdapter); !ok {
		t.Fatalf("configured adapter = %T, want *CloudAdapter", NewDefaultAdapter(t.TempDir()))
	}
}
