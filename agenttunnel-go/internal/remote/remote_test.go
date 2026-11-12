package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// fakeAdapter records what the bridge asked it to do. Hooks let a test drive
// pairing callbacks synchronously.
type fakeAdapter struct {
	mu          sync.Mutex
	connects    int
	disconnects int
	sent        []string
	sentIDs     []string
	inbound     func(InboundMessage)
	connectHook func(onQR func(string), onLinked func(string), onError func(error)) error
	connectCh   chan string
	sentCh      chan string
}

func (f *fakeAdapter) Connect(_ context.Context, onQR, onLinked func(string), onError func(error)) error {
	f.mu.Lock()
	f.connects++
	hook := f.connectHook
	connectCh := f.connectCh
	f.mu.Unlock()
	var err error
	if hook != nil {
		err = hook(onQR, onLinked, onError)
	} else {
		onLinked("fake device")
	}
	// Signal only after the callbacks have run: await(connectCh) then means the
	// bridge has persisted its state, not merely entered Connect.
	if connectCh != nil {
		connectCh <- "connect"
	}
	return err
}

func (f *fakeAdapter) Disconnect() error {
	f.mu.Lock()
	f.disconnects++
	f.mu.Unlock()
	return nil
}

func (f *fakeAdapter) Send(ctx context.Context, text string) error {
	_, err := f.SendTracked(ctx, text)
	return err
}

func (f *fakeAdapter) SendTracked(_ context.Context, text string) (string, error) {
	f.mu.Lock()
	f.sent = append(f.sent, text)
	id := fmt.Sprintf("out-%d", len(f.sent))
	f.sentIDs = append(f.sentIDs, id)
	ch := f.sentCh
	f.mu.Unlock()
	if ch != nil {
		ch <- text
	}
	return id, nil
}

func (f *fakeAdapter) SetInbound(handler func(InboundMessage)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbound = handler
}

// deliver hands one message to the handler the bridge installed, reporting
// whether there was one.
func (f *fakeAdapter) deliver(msg InboundMessage) bool {
	f.mu.Lock()
	handler := f.inbound
	f.mu.Unlock()
	if handler == nil {
		return false
	}
	handler(msg)
	return true
}

func (f *fakeAdapter) sends() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeAdapter) lastSentID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sentIDs) == 0 {
		return ""
	}
	return f.sentIDs[len(f.sentIDs)-1]
}

func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func newBridge(t *testing.T, dir string, adapter Adapter, onChange func()) *Bridge {
	t.Helper()
	b, err := New(Options{StateDir: dir, Adapter: adapter, OnChange: onChange})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

// Connecting without a QR adapter links immediately and is remembered across
// restarts.
func TestBridgeConnectsAndReconnects(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first := &fakeAdapter{}
	b := newBridge(t, dir, first, nil)
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := b.Status().State; got != string(StateConnected) {
		t.Fatalf("state after connect = %q, want connected", got)
	}

	var stored storedState
	data, err := os.ReadFile(filepath.Join(dir, "whatsapp.json"))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if err := json.Unmarshal(data, &stored); err != nil || !stored.Enabled {
		t.Fatalf("whatsapp.json should remember the device is enabled, got %s", data)
	}
	b.Close()

	second := &fakeAdapter{connectCh: make(chan string, 1)}
	b2 := newBridge(t, dir, second, nil)
	b2.Start(ctx)
	await(t, second.connectCh, "the saved device to reconnect")
	if got := b2.Status().State; got != string(StateConnected) {
		t.Fatalf("state after restart = %q, want connected", got)
	}
}

// A linked-device adapter surfaces its QR, then the link transitions to
// connected with the QR cleared.
func TestBridgePairsByQR(t *testing.T) {
	ctx := context.Background()
	changes := make(chan struct{}, 8)
	var link func(string)
	adapter := &fakeAdapter{connectHook: func(onQR func(string), onLinked func(string), _ func(error)) error {
		onQR("2@qr-payload")
		link = onLinked
		return nil
	}}
	b := newBridge(t, t.TempDir(), adapter, func() { changes <- struct{}{} })

	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := b.Status(); got.State != string(StatePairing) || got.QR != "2@qr-payload" {
		t.Fatalf("while pairing, status = %+v, want state pair+qr", got)
	}
	await(t, changes, "the pairing state to reach listeners")

	if link == nil {
		t.Fatal("adapter never handed out its link callback")
	}
	link("Cloud API · +1 555 0100")
	if got := b.Status(); got.State != string(StateConnected) || got.QR != "" {
		t.Fatalf("after linking, status = %+v, want connected with no QR", got)
	}
	await(t, changes, "the connected state to reach listeners")
}

// Disconnecting clears the item and records that the user wants no updates.
func TestBridgeDisconnectTurnsUpdatesOff(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	adapter := &fakeAdapter{}
	b := newBridge(t, dir, adapter, nil)
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := b.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if got := b.Status(); got.State != string(StateOff) || got.QR != "" {
		t.Fatalf("status after disconnect = %+v, want off", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "whatsapp.json"))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var stored storedState
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if stored.Enabled {
		t.Fatalf("disconnect must persist enabled=false, got %s", data)
	}

	adapter.mu.Lock()
	disconnects := adapter.disconnects
	adapter.mu.Unlock()
	if disconnects != 1 {
		t.Fatalf("adapter disconnects = %d, want 1", disconnects)
	}
}

// The bridge forwards a turn's final summary, an agent failure, and a question
// from the engine's typed events.
func TestBridgeForwardsWorkflowUpdates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	threadID := contracts.ThreadID("th_whatsapp")
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: threadID, Title: "Fix the flaky test",
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	adapter := &fakeAdapter{sentCh: make(chan string, 8)}
	b, err := New(Options{StateDir: t.TempDir(), Engine: eng, Adapter: adapter})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	b.Start(ctx)
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c2", Type: contracts.CmdMessageAppend, ThreadID: threadID,
		Message: &contracts.MessageSent{MessageID: "m1", Author: "orchestrator", Text: "All tests pass."},
	}); err != nil {
		t.Fatalf("turn summary: %v", err)
	}
	turn := await(t, adapter.sentCh, "the turn-complete update")
	if !strings.Contains(turn, "Fix the flaky test") || !strings.Contains(turn, "All tests pass.") {
		t.Fatalf("turn update = %q, want the title and summary", turn)
	}

	eng.Publish(threadID, contracts.AgentStatusChanged{
		AgentID: "agent-2", Status: contracts.AgentFailed, Reason: "harness exited 1",
	})
	failure := await(t, adapter.sentCh, "the failure update")
	if !strings.Contains(failure, "agent-2") || !strings.Contains(failure, "harness exited 1") {
		t.Fatalf("failure update = %q", failure)
	}

	eng.Publish(threadID, contracts.QuestionAsked{
		QuestionID: "q1", AgentID: "agent-3", Kind: contracts.QuestionChoice,
		Question: "Ship it?", Options: []string{"yes", "no"},
	})
	question := await(t, adapter.sentCh, "the question update")
	if !strings.Contains(question, "Ship it?") || !strings.Contains(question, "yes") {
		t.Fatalf("question update = %q", question)
	}
}

// Only a turn's final orchestrator summary is an update: ordinary messages,
// failed sub-steps of other kinds, and subagent chatter stay on the desktop.
func TestBridgeUpdatePolicy(t *testing.T) {
	b := newBridge(t, t.TempDir(), &fakeAdapter{}, nil)
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	cases := []struct {
		name    string
		payload contracts.EventPayload
		want    bool
	}{
		{"user message", contracts.MessageSent{Author: "user", Text: "hi"}, false},
		{"lead summary", contracts.MessageSent{Author: "orchestrator", Text: "done"}, true},
		{"council", contracts.MessageSent{Author: "council", Text: "consensus"}, false},
		{"agent done", contracts.AgentStatusChanged{AgentID: "a", Status: contracts.AgentSucceeded}, false},
		{"agent failed", contracts.AgentStatusChanged{AgentID: "a", Status: contracts.AgentFailed}, true},
		{"question", contracts.QuestionAsked{Question: "go?"}, true},
	}
	for _, tc := range cases {
		got := b.updateFor(contracts.Event{ThreadID: "th_x", Payload: tc.payload}) != ""
		if got != tc.want {
			t.Errorf("%s: notified = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Updates are dropped unless the device is linked.
func TestBridgeDropsUpdatesWhenOff(t *testing.T) {
	adapter := &fakeAdapter{}
	b := newBridge(t, t.TempDir(), adapter, nil)
	b.send(context.Background(), "should not go out")
	if got := adapter.sends(); len(got) != 0 {
		t.Fatalf("sent while off: %v", got)
	}
}

// Enqueueing never blocks the event reader: a full queue drops instead.
func TestBridgeEnqueueNeverBlocks(t *testing.T) {
	b := newBridge(t, t.TempDir(), &fakeAdapter{}, nil)
	for i := 0; i < updateQueue*2; i++ {
		b.enqueue("update")
	}
	if got := len(b.out); got != updateQueue {
		t.Fatalf("queued = %d, want the bounded %d", got, updateQueue)
	}
}

// A callback from a connect attempt that a disconnect superseded must not
// revive the item.
func TestBridgeIgnoresStaleAdapterCallbacks(t *testing.T) {
	ctx := context.Background()
	var qr, link func(string)
	adapter := &fakeAdapter{connectHook: func(onQR func(string), onLinked func(string), _ func(error)) error {
		qr, link = onQR, onLinked
		return nil
	}}
	b := newBridge(t, t.TempDir(), adapter, nil)
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := b.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if qr == nil || link == nil {
		t.Fatal("adapter never handed out its callbacks")
	}
	qr("stale-qr")
	link("stale link")
	if got := b.Status(); got.State != string(StateOff) || got.QR != "" {
		t.Fatalf("stale callback changed status: %+v", got)
	}
}

// An adapter that fails after linking (the phone unlinked this device, or the
// QR expired) must move the row to error, clear the QR, notify listeners, and
// leave a later Connect free to start over.
func TestBridgeAdapterAsyncErrorBecomesAnErrorRow(t *testing.T) {
	var fail func(error)
	changes := make(chan struct{}, 8)
	adapter := &fakeAdapter{connectHook: func(_ func(string), onLinked func(string), onError func(error)) error {
		fail = onError
		onLinked("fake device")
		return nil
	}}
	b := newBridge(t, t.TempDir(), adapter, func() { changes <- struct{}{} })
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := b.Status().State; got != string(StateConnected) {
		t.Fatalf("state after connect = %q, want connected", got)
	}
	for len(changes) > 0 {
		<-changes
	}

	boom := errors.New("the device was unlinked from the phone; pair it again")
	fail(boom)
	got := b.Status()
	if got.State != string(StateError) || got.Detail != boom.Error() || got.QR != "" {
		t.Fatalf("status after async error = %+v, want error with the message and no QR", got)
	}
	await(t, changes, "the error state to reach listeners")

	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	adapter.mu.Lock()
	connects := adapter.connects
	adapter.mu.Unlock()
	if connects != 2 {
		t.Fatalf("adapter connects = %d, want a new attempt after the error", connects)
	}
}

// A second Connect while pairing is already in flight must not start another
// link attempt.
func TestBridgeSecondConnectWhilePairingStartsOneLinker(t *testing.T) {
	adapter := &fakeAdapter{connectHook: func(onQR func(string), _ func(string), _ func(error)) error {
		onQR("2@qr")
		return nil
	}}
	b := newBridge(t, t.TempDir(), adapter, nil)
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := b.Status().State; got != string(StatePairing) {
		t.Fatalf("state = %q, want pairing", got)
	}
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	adapter.mu.Lock()
	connects := adapter.connects
	adapter.mu.Unlock()
	if connects != 1 {
		t.Fatalf("adapter connects = %d, want 1", connects)
	}
}

// A failed connect keeps the enabled choice: the user asked for updates, so the
// next daemon start retries instead of silently forgetting it.
func TestBridgeFailedConnectKeepsUpdatesEnabled(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("pairing refused")
	failing := &fakeAdapter{connectHook: func(func(string), func(string), func(error)) error { return boom }}
	b := newBridge(t, dir, failing, nil)
	if err := b.Connect(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("connect = %v, want the adapter error", err)
	}
	if got := b.Status().State; got != string(StateError) {
		t.Fatalf("state = %q, want error", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, "whatsapp.json"))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var stored storedState
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if !stored.Enabled {
		t.Fatalf("a failed connect must keep enabled=true, got %s", data)
	}

	retry := &fakeAdapter{connectCh: make(chan string, 1)}
	b2 := newBridge(t, dir, retry, nil)
	b2.Start(context.Background())
	await(t, retry.connectCh, "the saved device to retry")
	if got := b2.Status().State; got != string(StateConnected) {
		t.Fatalf("state after restart = %q, want connected", got)
	}
}

// closeOnlyAdapter is an adapter whose shutdown path stops the socket rather
// than unlinking the device, like the linked-device adapter.
type closeOnlyAdapter struct {
	fakeAdapter
	closes int
}

func (a *closeOnlyAdapter) Close() {
	a.mu.Lock()
	a.closes++
	a.mu.Unlock()
}

// Closing the bridge must not unlink a device: the saved session is what makes
// the next daemon start reconnect.
func TestBridgeCloseStopsWithoutUnlinking(t *testing.T) {
	adapter := &closeOnlyAdapter{}
	b := newBridge(t, t.TempDir(), adapter, nil)
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	b.Close()

	adapter.mu.Lock()
	disconnects, closes := adapter.disconnects, adapter.closes
	adapter.mu.Unlock()
	if closes != 1 || disconnects != 0 {
		t.Fatalf("bridge close = closes %d, disconnects %d; want 1 and 0", closes, disconnects)
	}
}

// Close is safe to call twice: the adapter unlinks once.
func TestBridgeCloseIsIdempotent(t *testing.T) {
	adapter := &fakeAdapter{}
	b := newBridge(t, t.TempDir(), adapter, nil)
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	b.Close()
	b.Close()
	adapter.mu.Lock()
	disconnects := adapter.disconnects
	adapter.mu.Unlock()
	if disconnects != 1 {
		t.Fatalf("adapter disconnects = %d, want 1", disconnects)
	}
}

// Without an adapter the item is honestly unavailable, not a toggle that can
// only fail.
func TestBridgeWithoutAdapterReportsUnavailable(t *testing.T) {
	b, err := New(Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	item := b.Status()
	if !item.Unavailable || item.State != string(StateOff) {
		t.Fatalf("status = %+v, want off and unavailable", item)
	}
	if !strings.Contains(item.Detail, "Not configured") {
		t.Fatalf("detail = %q, want the configuration hint", item.Detail)
	}
	if err := b.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("connect without an adapter = %v, want the configuration error", err)
	}
}

// inboundBridge is a bridge whose Control handler reports every routed message
// on a channel and replies with a fixed text.
type inboundBridge struct {
	bridge  *Bridge
	adapter *fakeAdapter
	routed  chan string
}

const inboundReply = "handled"

func newInboundBridge(t *testing.T, dir string) *inboundBridge {
	t.Helper()
	routed := make(chan string, 16)
	adapter := &fakeAdapter{sentCh: make(chan string, 16)}
	b, err := New(Options{
		StateDir: dir,
		Adapter:  adapter,
		Control: func(_ context.Context, text string) string {
			routed <- text
			return inboundReply
		},
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	return &inboundBridge{bridge: b, adapter: adapter, routed: routed}
}

// Control is off by default, is separate from notifications, and the bridge
// ignores inbound messages entirely while it is off.
func TestBridgeInboundControlOffByDefault(t *testing.T) {
	dir := t.TempDir()
	f := newInboundBridge(t, dir)
	if f.bridge.Control() {
		t.Fatal("control must default off")
	}
	if err := f.bridge.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Connecting turns notifications on; control stays off.
	if got := f.bridge.Status(); got.State != string(StateConnected) || got.Control {
		t.Fatalf("status after connect = %+v, want connected with control off", got)
	}

	if text, ok := f.bridge.acceptInbound(InboundMessage{ID: "m1", Text: "/threads", FromSelf: true}); ok {
		t.Fatalf("control off accepted %q", text)
	}
	// The drop happens synchronously, so no router call can be in flight.
	if !f.adapter.deliver(InboundMessage{ID: "m1", Text: "/threads", FromSelf: true}) {
		t.Fatal("the bridge never installed its inbound handler")
	}
	select {
	case got := <-f.routed:
		t.Fatalf("control off routed %q", got)
	default:
	}
}

// Self-chat only, echo suppression by id and text, dedupe by id, and a persisted
// setting that survives a restart.
func TestBridgeInboundPolicy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := newInboundBridge(t, dir)
	if err := f.bridge.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := f.bridge.SetControl(true); err != nil {
		t.Fatalf("set control: %v", err)
	}

	var stored storedState
	data, err := os.ReadFile(filepath.Join(dir, "whatsapp.json"))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if err := json.Unmarshal(data, &stored); err != nil || !stored.Control || !stored.Enabled {
		t.Fatalf("whatsapp.json = %s, want both choices remembered", data)
	}

	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: "m0", Text: "hi", FromSelf: false}); ok {
		t.Fatal("a message from another chat must not be routed")
	}
	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: "m0", Text: "   ", FromSelf: true}); ok {
		t.Fatal("an empty message must not be routed")
	}

	text, ok := f.bridge.acceptInbound(InboundMessage{ID: "m1", Text: " hello ", FromSelf: true})
	if !ok || text != "hello" {
		t.Fatalf("acceptInbound = %q, %v; want trimmed text", text, ok)
	}
	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: "m1", Text: "hello", FromSelf: true}); ok {
		t.Fatal("the same message id must be routed once")
	}

	// An update the bridge sent is remembered by id and by text.
	f.bridge.send(ctx, "turn complete")
	sentID := f.adapter.lastSentID()
	if sentID == "" {
		t.Fatal("send did not record a message id")
	}
	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: sentID, Text: "something else", FromSelf: true}); ok {
		t.Fatal("the echo of the bridge's own update must be dropped by id")
	}
	echo := f.adapter.sends()[0]
	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: "m2", Text: echo, FromSelf: true}); ok {
		t.Fatal("the echo of the bridge's own update must be dropped by text")
	}

	// A restart loads the control choice; notifications were enabled too.
	f.bridge.Close()
	restarted := newInboundBridge(t, dir)
	if !restarted.bridge.Control() {
		t.Fatal("control must survive a restart")
	}
	if !restarted.bridge.Status().Control {
		t.Fatal("status must report control so the UI can show the toggle")
	}
}

// The text fallback expires while the recorded id stays authoritative, so a
// command that repeats an older update is not silently dropped forever.
func TestBridgeEchoTextExpires(t *testing.T) {
	f := newInboundBridge(t, t.TempDir())
	if err := f.bridge.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := f.bridge.SetControl(true); err != nil {
		t.Fatalf("set control: %v", err)
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	f.bridge.now = func() time.Time { return now }

	f.bridge.send(context.Background(), "turn complete")
	sentID := f.adapter.lastSentID()
	echo := f.adapter.sends()[0]

	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: "m1", Text: echo, FromSelf: true}); ok {
		t.Fatal("the immediate echo of an update must be dropped")
	}
	// The marker drops an update whatever the text window says, so an old
	// update's echo never becomes a command.
	now = now.Add(echoTextWindow + time.Second)
	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: "m2", Text: echo, FromSelf: true}); ok {
		t.Fatal("an update's echo must stay dropped after the text window")
	}
	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: "m3", Text: "turn complete", FromSelf: true}); !ok {
		t.Fatal("an unmarked text match must not hide a later command")
	}
	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: sentID, Text: "anything", FromSelf: true}); ok {
		t.Fatal("the recorded message id must drop the echo regardless of age")
	}
}

// One message reaches the router and its reply goes back through the bridge's
// normal sender.
func TestBridgeRoutesInboundAndReplies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	f := newInboundBridge(t, t.TempDir())
	f.bridge.Start(ctx)
	if err := f.bridge.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := f.bridge.SetControl(true); err != nil {
		t.Fatalf("set control: %v", err)
	}

	if !f.adapter.deliver(InboundMessage{ID: "m1", Text: "/threads", FromSelf: true}) {
		t.Fatal("the bridge never installed its inbound handler")
	}
	if got := await(t, f.routed, "the message to reach the router"); got != "/threads" {
		t.Fatalf("routed = %q, want the message text", got)
	}
	if got := await(t, f.adapter.sentCh, "the router's reply"); got != outboundMarker+" "+inboundReply {
		t.Fatalf("reply = %q, want the marked reply", got)
	}
}

// Accepted messages are routed by one worker in arrival order: "/use 2" has
// committed before the following text reads the selection, and no two commands
// ever run at once. Everything is driven by typed channels, never a sleep.
func TestBridgeSerializesInboundRouting(t *testing.T) {
	const (
		selectText = "/use 2"
		actText    = "run the tests"
	)
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	routed := make(chan string, 4)
	var active, overlapped int32
	var mu sync.Mutex
	selected := ""

	adapter := &fakeAdapter{sentCh: make(chan string, 8)}
	b, err := New(Options{
		StateDir: t.TempDir(),
		Adapter:  adapter,
		Control: func(_ context.Context, text string) string {
			if atomic.AddInt32(&active, 1) > 1 {
				atomic.StoreInt32(&overlapped, 1)
			}
			defer atomic.AddInt32(&active, -1)
			if text == selectText {
				close(firstEntered)
				<-release
				mu.Lock()
				selected = "t2"
				mu.Unlock()
				return ""
			}
			mu.Lock()
			got := selected
			mu.Unlock()
			routed <- got + ":" + text
			return ""
		},
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := b.SetControl(true); err != nil {
		t.Fatalf("set control: %v", err)
	}

	if !adapter.deliver(InboundMessage{ID: "m1", Text: selectText, FromSelf: true}) {
		t.Fatal("the bridge never installed its inbound handler")
	}
	// Wait until the first command is inside the router, then deliver the next
	// one while it is still running. It must wait its turn.
	<-firstEntered
	if !adapter.deliver(InboundMessage{ID: "m2", Text: actText, FromSelf: true}) {
		t.Fatal("the bridge never installed its inbound handler")
	}
	close(release)

	if got := await(t, routed, "the second command to be routed"); got != "t2:"+actText {
		t.Fatalf("routed = %q, want it to see the selection made by the first command", got)
	}
	if atomic.LoadInt32(&overlapped) != 0 {
		t.Fatal("two commands were routed concurrently")
	}
}

// ControlSupported reports whether this configuration can receive control
// messages at all, so the UI never offers an inert toggle.
type sendOnlyAdapter struct{}

func (sendOnlyAdapter) Connect(context.Context, func(string), func(string), func(error)) error {
	return nil
}
func (sendOnlyAdapter) Disconnect() error                  { return nil }
func (sendOnlyAdapter) Send(context.Context, string) error { return nil }

func TestBridgeReportsControlCapability(t *testing.T) {
	supported := newInboundBridge(t, t.TempDir())
	if !supported.bridge.Status().ControlSupported {
		t.Fatal("a bridge whose adapter can receive messages must advertise control")
	}

	sendOnly, err := New(Options{
		StateDir: t.TempDir(),
		Adapter:  sendOnlyAdapter{},
		Control:  func(context.Context, string) string { return "" },
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(sendOnly.Close)
	if sendOnly.Status().ControlSupported {
		t.Fatal("an adapter with no inbound path must not advertise control")
	}
}

// Inbound control needs a linked device: a saved handler alone routes nothing.
func TestBridgeInboundRequiresALinkedDevice(t *testing.T) {
	f := newInboundBridge(t, t.TempDir())
	if err := f.bridge.SetControl(true); err != nil {
		t.Fatalf("set control: %v", err)
	}
	if _, ok := f.bridge.acceptInbound(InboundMessage{ID: "m1", Text: "hi", FromSelf: true}); ok {
		t.Fatal("an unlinked bridge must not route inbound messages")
	}
}

// newRoutedBridge is a connected, control-enabled bridge whose router reports
// each command it runs. The name is written to whatsapp.json first, the same way
// a user would configure it; "" is an unnamed instance.
func newRoutedBridge(t *testing.T, name string) (*Bridge, *fakeAdapter, chan string) {
	t.Helper()
	dir := t.TempDir()
	data := fmt.Sprintf(`{"enabled":true,"control":true,"name":%q}`, name)
	if err := os.WriteFile(filepath.Join(dir, "whatsapp.json"), []byte(data), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	routed := make(chan string, 16)
	adapter := &fakeAdapter{}
	b, err := New(Options{
		StateDir: dir,
		Adapter:  adapter,
		Control: func(_ context.Context, text string) string {
			routed <- text
			return ""
		},
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return b, adapter, routed
}

// Two instances share one account. Each one's update arrives at the other as a
// self-message it did not send; neither starts a turn, and the other's real
// command still runs, which proves the updates were dropped rather than queued.
func TestSharedAccountUpdatesStartNoTurns(t *testing.T) {
	a, aAdapter, aRouted := newRoutedBridge(t, "")
	b, bAdapter, bRouted := newRoutedBridge(t, "")

	a.send(context.Background(), "a: turn complete")
	b.send(context.Background(), "b: turn complete")
	aUpdate, bUpdate := aAdapter.sends()[0], bAdapter.sends()[0]
	if !strings.HasPrefix(aUpdate, outboundMarker) || !strings.HasPrefix(bUpdate, outboundMarker) {
		t.Fatalf("updates must carry the marker: %q, %q", aUpdate, bUpdate)
	}

	// The updates reach the other instance under fresh ids it never recorded.
	if !bAdapter.deliver(InboundMessage{ID: "from-a", Text: aUpdate, FromSelf: true}) {
		t.Fatal("bridge b never installed its inbound handler")
	}
	if !aAdapter.deliver(InboundMessage{ID: "from-b", Text: bUpdate, FromSelf: true}) {
		t.Fatal("bridge a never installed its inbound handler")
	}
	// Barrier: a real command on each side is routed after the updates would
	// have been, so any update that was routed would show up first.
	bAdapter.deliver(InboundMessage{ID: "cmd-b", Text: "/threads", FromSelf: true})
	aAdapter.deliver(InboundMessage{ID: "cmd-a", Text: "/threads", FromSelf: true})
	if got := await(t, bRouted, "bridge b's command"); got != "/threads" {
		t.Fatalf("bridge b routed %q first, want its command; an update started a turn", got)
	}
	if got := await(t, aRouted, "bridge a's command"); got != "/threads" {
		t.Fatalf("bridge a routed %q first, want its command; an update started a turn", got)
	}
}

// A self-message that starts with the marker is never a command, whether it
// came from another instance or is an echo this one lost the id for.
func TestMarkedSelfMessagesAreIgnored(t *testing.T) {
	b, _, _ := newRoutedBridge(t, "")
	for _, text := range []string{
		outboundMarker + " turn complete",
		outboundMarker + " [work] /threads",
		outboundMarker,
	} {
		if got, ok := b.acceptInbound(InboundMessage{ID: "m-" + text, Text: text, FromSelf: true}); ok {
			t.Fatalf("marked message %q routed as %q", text, got)
		}
	}
	if got, ok := b.acceptInbound(InboundMessage{ID: "m-plain", Text: "/threads", FromSelf: true}); !ok || got != "/threads" {
		t.Fatalf("plain command = %q, %v; want it routed", got, ok)
	}
}

// "@name" routes a command to exactly the named instance. An unnamed instance
// takes only unaddressed commands, so it ignores anything addressed to a name.
func TestNamedRoutingHitsOnlyTheAddressedInstance(t *testing.T) {
	_, workAdapter, workRouted := newRoutedBridge(t, "work")
	_, homeAdapter, homeRouted := newRoutedBridge(t, "home")
	_, plainAdapter, plainRouted := newRoutedBridge(t, "")

	deliver := func(adapter *fakeAdapter, id, text string) {
		t.Helper()
		if !adapter.deliver(InboundMessage{ID: id, Text: text, FromSelf: true}) {
			t.Fatal("bridge never installed its inbound handler")
		}
	}
	deliver(workAdapter, "1", "@work /threads")
	deliver(homeAdapter, "2", "@work /threads")
	deliver(plainAdapter, "3", "@work /threads")
	deliver(homeAdapter, "4", "@home run the tests")
	deliver(workAdapter, "5", "@home run the tests")
	deliver(plainAdapter, "6", "@home run the tests")
	deliver(workAdapter, "7", "run the tests")
	deliver(homeAdapter, "8", "run the tests")
	deliver(plainAdapter, "9", "run the tests")

	// Barrier per instance: every addressed message was queued before the
	// barrier, so a wrong route would surface before it.
	deliver(workAdapter, "w-end", "@work done")
	deliver(homeAdapter, "h-end", "@home done")
	deliver(plainAdapter, "p-end", "done")

	if got := await(t, workRouted, "work's command"); got != "/threads" {
		t.Fatalf("work routed %q, want /threads", got)
	}
	if got := await(t, workRouted, "work's barrier"); got != "done" {
		t.Fatalf("work routed %q after /threads, want only its own commands", got)
	}
	if got := await(t, homeRouted, "home's command"); got != "run the tests" {
		t.Fatalf("home routed %q, want run the tests", got)
	}
	if got := await(t, homeRouted, "home's barrier"); got != "done" {
		t.Fatalf("home routed %q after its command, want only its own commands", got)
	}
	if got := await(t, plainRouted, "plain's command"); got != "run the tests" {
		t.Fatalf("unnamed instance routed %q, want the unaddressed command", got)
	}
	if got := await(t, plainRouted, "plain's barrier"); got != "done" {
		t.Fatalf("unnamed instance routed %q, want only unaddressed commands", got)
	}
}

// The name is persisted with the other choices so a restart keeps the address.
func TestNameSurvivesRestartAndIsTrimmed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "whatsapp.json"), []byte(`{"enabled":false,"name":"  work  "}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	b, err := New(Options{StateDir: dir, Adapter: &fakeAdapter{}})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	if err := b.SetControl(true); err != nil {
		t.Fatalf("set control: %v", err)
	}
	var stored storedState
	data, _ := os.ReadFile(filepath.Join(dir, "whatsapp.json"))
	if err := json.Unmarshal(data, &stored); err != nil || stored.Name != "work" {
		t.Fatalf("whatsapp.json = %s, want the trimmed name kept", data)
	}
}

// A periodic status report reaches the linked chat as a turn update, titled by
// its thread, and is not treated as an inbound command when WhatsApp echoes it.
func TestBridgeForwardsStatusReports(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	threadID := contracts.ThreadID("th_status")
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: threadID, Title: "Ship the report",
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	routed := make(chan string, 4)
	adapter := &fakeAdapter{sentCh: make(chan string, 8)}
	b, err := New(Options{
		StateDir: t.TempDir(), Engine: eng, Adapter: adapter,
		Control: func(_ context.Context, text string) string {
			routed <- text
			return ""
		},
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	b.Start(ctx)
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := b.SetControl(true); err != nil {
		t.Fatalf("set control: %v", err)
	}

	const report = "Workflow status · 1m0s elapsed\n\nDone\n- plan"
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "c2", Type: contracts.CmdMessageAppend, ThreadID: threadID,
		Message: &contracts.MessageSent{MessageID: "s1", Author: contracts.AuthorStatus, Text: report},
	}); err != nil {
		t.Fatalf("status report: %v", err)
	}
	got := await(t, adapter.sentCh, "the status report")
	if !strings.HasPrefix(got, outboundMarker+" Ship the report — status update\n\n") || !strings.Contains(got, report) {
		t.Fatalf("status update = %q, want the marker, the title, then the report", got)
	}

	// WhatsApp echoes the bridge's own update back. It must not reach the router:
	// the next real command is routed first, which proves the echo was dropped.
	if !adapter.deliver(InboundMessage{ID: "echo", Text: got, FromSelf: true}) {
		t.Fatal("the bridge never installed its inbound handler")
	}
	if !adapter.deliver(InboundMessage{ID: "cmd", Text: "/threads", FromSelf: true}) {
		t.Fatal("the bridge never installed its inbound handler")
	}
	if first := await(t, routed, "the next command to reach the router"); first != "/threads" {
		t.Fatalf("routed %q first, want the status echo to be dropped", first)
	}
}
