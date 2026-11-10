package relay

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const (
	testInstance = "0123456789abcdef0123456789abcdef"
	testSecret   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// clock is the injected time source. Tests advance it instead of sleeping.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// startRelay runs a relay on 127.0.0.1:0 and returns its ws base URL. Every
// request URL is recorded so tests can assert no secret ever appears in one.
func startRelay(t *testing.T, opts Options) (*Server, string, *clock, *urlLog) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	opts.Now = clk.Now
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	urls := &urlLog{}
	httpSrv := httptest.NewUnstartedServer(urls.wrap(srv.Handler()))
	httpSrv.Listener.Close()
	httpSrv.Listener = ln
	httpSrv.Start()
	t.Cleanup(httpSrv.Close)
	return srv, "ws://" + ln.Addr().String(), clk, urls
}

type urlLog struct {
	mu   sync.Mutex
	seen []string
}

func (u *urlLog) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.seen = append(u.seen, r.URL.String())
		u.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

func (u *urlLog) all() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, frame(v)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// read returns the next frame, failing the test if none arrives in time.
func read(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	m, err := tryRead(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}

func tryRead(c *websocket.Conn) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// readType skips frames until one with the wanted control type arrives.
func readType(t *testing.T, c *websocket.Conn, typ string) map[string]any {
	t.Helper()
	for {
		m := read(t, c)
		if m["t"] == typ {
			return m
		}
	}
}

// readData skips control frames and returns the next JSON-RPC frame, which has
// no `t` field.
func readData(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	for {
		m := read(t, c)
		if _, hasT := m["t"]; !hasT {
			return m
		}
	}
}

// connectDaemon opens a daemon socket and waits for its hello ack.
func connectDaemon(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	d := dial(t, base+"/v1/daemon")
	send(t, d, Hello{T: tHello, Role: "daemon", Instance: testInstance, Secret: testSecret, Name: "Desk"})
	ack := readType(t, d, tAck)
	if ack["ok"] != true {
		t.Fatalf("daemon hello rejected: %v", ack["error"])
	}
	return d
}

// offerCode sends a pairing code from the daemon and waits for a ping reply. Frames
// on one socket are handled in order, so the pong proves the offer is live.
func offerCode(t *testing.T, d *websocket.Conn, code string) {
	t.Helper()
	send(t, d, PairOffer{T: tPairOffer, Code: code})
	send(t, d, map[string]string{"t": tPing})
	readType(t, d, tPong)
}

// pairWith redeems a code over a fresh client socket and returns the ack.
func pairWith(t *testing.T, base, code string) map[string]any {
	t.Helper()
	c := dial(t, base+"/v1/client")
	send(t, c, Pair{T: tPair, Code: code, Name: "Pixel 9"})
	return readType(t, c, tAck)
}

// attachWith opens a client socket with a token and attaches it.
func attachWith(t *testing.T, base, token string) (*websocket.Conn, map[string]any) {
	t.Helper()
	c := dial(t, base+"/v1/client")
	send(t, c, Hello{T: tHello, Role: "client", Token: token})
	if ack := readType(t, c, tAck); ack["ok"] != true {
		return c, ack
	}
	send(t, c, Attach{T: tAttach})
	return c, readType(t, c, tAck)
}

func TestPairCodeIsSingleUse(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	code, err := NewCode()
	if err != nil {
		t.Fatal(err)
	}
	offerCode(t, d, code)

	first := pairWith(t, base, code)
	if first["ok"] != true || first["token"] == "" {
		t.Fatalf("first redeem should succeed, got %v", first)
	}
	second := pairWith(t, base, code)
	if second["ok"] == true || second["error"] != ErrBadCode {
		t.Fatalf("reused code must fail as bad_code, got %v", second)
	}
}

func TestPairCodeExpires(t *testing.T) {
	_, base, clk, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")

	clk.Advance(CodeTTL)
	expired := pairWith(t, base, "ABCDE-FGHJK")
	if expired["ok"] == true || expired["error"] != ErrBadCode {
		t.Fatalf("expired code must fail as bad_code, got %v", expired)
	}
}

func TestNewOfferSupersedesLiveCode(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	offerCode(t, d, "MNPQR-STVWX")

	if old := pairWith(t, base, "ABCDE-FGHJK"); old["error"] != ErrBadCode {
		t.Fatalf("superseded code must not redeem, got %v", old)
	}
	if fresh := pairWith(t, base, "mnpqr stvwx"); fresh["ok"] != true {
		t.Fatalf("live code should redeem regardless of case or spacing, got %v", fresh)
	}
}

func TestPairRateLimitAfterFiveFailures(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")

	for i := 0; i < MaxFailedPerIP; i++ {
		if got := pairWith(t, base, "ZZZZZ-ZZZZZ"); got["error"] != ErrBadCode {
			t.Fatalf("attempt %d: want bad_code, got %v", i+1, got)
		}
	}
	if got := pairWith(t, base, "ABCDE-FGHJK"); got["error"] != ErrRateLimited {
		t.Fatalf("sixth attempt must be rate limited even with the right code, got %v", got)
	}
}

func TestPairCodeInvalidatedAfterTwentyFailures(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)}
	srv, err := New(Options{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	srv.offers[testInstance] = &offer{hash: hashHex("ABCDE-FGHJK"), expires: clk.Now().Add(CodeTTL)}
	srv.instances[testInstance] = &instanceRecord{ID: testInstance}

	// Spread failures across IPs so the per-IP limit does not interfere.
	for i := 0; i < MaxFailedPerCode; i++ {
		srv.mu.Lock()
		_, ok := srv.redeemLocked("ZZZZZ-ZZZZZ", clk.Now())
		if !ok {
			srv.recordFailureLocked("10.0.0."+string(rune('a'+i)), clk.Now())
		}
		srv.mu.Unlock()
	}
	srv.mu.Lock()
	_, live := srv.offers[testInstance]
	srv.mu.Unlock()
	if live {
		t.Fatal("the 20th failed attempt must invalidate the live code")
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	c := dial(t, base+"/v1/client")
	send(t, c, Hello{T: tHello, Role: "client", Token: "not-a-real-token"})
	ack := readType(t, c, tAck)
	if ack["ok"] == true || ack["error"] != ErrBadToken {
		t.Fatalf("wrong token must fail as bad_token, got %v", ack)
	}
}

func TestDaemonWrongSecretIsRejected(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	connectDaemon(t, base)

	d := dial(t, base+"/v1/daemon")
	other := strings.Repeat("b", 64)
	send(t, d, Hello{T: tHello, Role: "daemon", Instance: testInstance, Secret: other})
	ack := readType(t, d, tAck)
	if ack["ok"] == true || ack["error"] != ErrBadSecret {
		t.Fatalf("wrong daemon secret must fail as bad_secret, got %v", ack)
	}
}

func TestTokenExpiresAfterTTL(t *testing.T) {
	_, base, clk, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	paired := pairWith(t, base, "ABCDE-FGHJK")
	token, _ := paired["token"].(string)

	clk.Advance(TokenTTL)
	c := dial(t, base+"/v1/client")
	send(t, c, Hello{T: tHello, Role: "client", Token: token})
	if ack := readType(t, c, tAck); ack["error"] != ErrBadToken {
		t.Fatalf("expired token must fail as bad_token, got %v", ack)
	}
}

func TestAttachRejectsAnotherInstance(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	token, _ := pairWith(t, base, "ABCDE-FGHJK")["token"].(string)

	c := dial(t, base+"/v1/client")
	send(t, c, Hello{T: tHello, Role: "client", Token: token})
	readType(t, c, tAck)
	send(t, c, Attach{T: tAttach, Instance: strings.Repeat("f", 32)})
	if ack := readType(t, c, tAck); ack["error"] != ErrBadInstance {
		t.Fatalf("a token bound to one instance must not attach to another, got %v", ack)
	}
}

func TestDataFramesRouteToTheTaggedClient(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	token, _ := pairWith(t, base, "ABCDE-FGHJK")["token"].(string)
	clientID := clientIDFor(t, base, token)

	phone, ack := attachWith(t, base, token)
	if ack["daemon"] != "online" {
		t.Fatalf("attach should report the daemon online, got %v", ack)
	}

	send(t, phone, map[string]any{"jsonrpc": "2.0", "id": "7", "method": "thread.list"})
	req := readData(t, d)
	if req["c"] != clientID || req["method"] != "thread.list" {
		t.Fatalf("daemon should see the request tagged with the client id, got %v", req)
	}

	send(t, d, map[string]any{"c": clientID, "id": "7", "result": map[string]any{"threads": []any{}}})
	reply := read(t, phone)
	if reply["id"] != "7" {
		t.Fatalf("reply should reach the phone, got %v", reply)
	}
}

func TestDaemonOfflineAnswersJSONRPCError(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	token, _ := pairWith(t, base, "ABCDE-FGHJK")["token"].(string)
	phone, _ := attachWith(t, base, token)

	// Drop the daemon, then the phone's request must fail with daemon_offline.
	d.CloseNow()
	status := readType(t, phone, tStatus)
	if status["daemon"] != "offline" {
		t.Fatalf("phone should learn the daemon went offline, got %v", status)
	}
	send(t, phone, map[string]any{"id": "9", "method": "thread.list"})
	reply := read(t, phone)
	errObj, _ := reply["error"].(map[string]any)
	if reply["id"] != "9" || errObj["message"] != msgDaemonOffline {
		t.Fatalf("want daemon_offline for id 9, got %v", reply)
	}
}

func TestUnpairClosesLiveSocketAndBlocksReconnect(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	paired := pairWith(t, base, "ABCDE-FGHJK")
	token, _ := paired["token"].(string)

	phone, _ := attachWith(t, base, token)
	clientID := clientIDFor(t, base, token)

	send(t, d, Revoke{T: tRevoke, ClientID: clientID})
	bye := readType(t, phone, tBye)
	if bye["reason"] != reasonRevoked {
		t.Fatalf("phone should see bye reason=revoked, got %v", bye)
	}
	if _, err := tryRead(phone); err == nil {
		t.Fatal("the revoked socket must close")
	}

	c := dial(t, base+"/v1/client")
	send(t, c, Hello{T: tHello, Role: "client", Token: token})
	if ack := readType(t, c, tAck); ack["error"] != ErrBadToken {
		t.Fatalf("revoked token must not reconnect, got %v", ack)
	}
}

// clientIDFor reads the client id a token maps to via a hello ack.
func clientIDFor(t *testing.T, base, token string) string {
	t.Helper()
	c := dial(t, base+"/v1/client")
	send(t, c, Hello{T: tHello, Role: "client", Token: token})
	id, _ := readType(t, c, tAck)["clientId"].(string)
	if id == "" {
		t.Fatal("hello ack carried no client id")
	}
	return id
}

func TestPhoneSelfRevokeNotifiesDaemon(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	token, _ := pairWith(t, base, "ABCDE-FGHJK")["token"].(string)
	clientID := clientIDFor(t, base, token)

	phone, _ := attachWith(t, base, token)
	send(t, phone, map[string]string{"t": tRevoke})
	unpaired := readType(t, d, tUnpaired)
	if unpaired["clientId"] != clientID {
		t.Fatalf("daemon should learn which phone unpaired, got %v", unpaired)
	}
}

func TestDaemonGetsDeviceSnapshotOnHello(t *testing.T) {
	_, base, _, _ := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	pairWith(t, base, "ABCDE-FGHJK")
	d.CloseNow()

	// The snapshot arrives on the next daemon hello, and it lists the phone.
	again := dial(t, base+"/v1/daemon")
	send(t, again, Hello{T: tHello, Role: "daemon", Instance: testInstance, Secret: testSecret})
	snap := readType(t, again, tDevices)
	devices, _ := snap["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("snapshot should list the one paired phone, got %v", snap)
	}
}

func TestRelayStoresNoFrameBodies(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	_, base, _, _ := startRelay(t, Options{StatePath: statePath})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	token, _ := pairWith(t, base, "ABCDE-FGHJK")["token"].(string)
	phone, _ := attachWith(t, base, token)

	const secretPrompt = "secret-prompt-body-do-not-persist"
	send(t, phone, map[string]any{"id": "1", "method": "turn.start", "params": map[string]string{"text": secretPrompt}})
	readData(t, d)

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if strings.Contains(string(data), secretPrompt) {
		t.Fatal("frame bodies must never reach the state file")
	}
}

func TestStateSurvivesRestartAndKeepsOnlyHashes(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	_, base, _, _ := startRelay(t, Options{StatePath: statePath})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	token, _ := pairWith(t, base, "ABCDE-FGHJK")["token"].(string)

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if strings.Contains(string(data), token) || strings.Contains(string(data), testSecret) {
		t.Fatal("state file must hold hashes, never the token or the daemon secret")
	}

	restarted, err := New(Options{StatePath: statePath})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rec, code := restarted.authTokenLocked(token); code != "" || rec == nil {
		t.Fatalf("restarted relay should still accept the token, got %q", code)
	}
}

func TestTokenNeverAppearsInAnyURL(t *testing.T) {
	_, base, _, urls := startRelay(t, Options{})
	d := connectDaemon(t, base)
	offerCode(t, d, "ABCDE-FGHJK")
	token, _ := pairWith(t, base, "ABCDE-FGHJK")["token"].(string)
	attachWith(t, base, token)

	for _, u := range urls.all() {
		if strings.Contains(u, token) || strings.Contains(u, "token=") || strings.Contains(u, "?") {
			t.Fatalf("handshake URL carries a secret: %s", u)
		}
	}
}

func TestValidateURL(t *testing.T) {
	ok := []string{"wss://relay.example.com", "ws://127.0.0.1:7777", "ws://localhost:1", "ws://192.168.1.20:7777/base/"}
	for _, u := range ok {
		if _, err := ValidateURL(u); err != nil {
			t.Errorf("%s should be accepted: %v", u, err)
		}
	}
	bad := []string{
		"ws://relay.example.com",          // plain ws to a public host
		"https://relay.example.com",       // wrong scheme
		"wss://relay.example.com?token=x", // secrets never in a URL
		"wss://user:pw@relay.example.com", // credentials in the URL
		"wss://",                          // no host
	}
	for _, u := range bad {
		if _, err := ValidateURL(u); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
}

func TestNewCodeFormatAndNormalize(t *testing.T) {
	code, err := NewCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 11 || code[5] != '-' {
		t.Fatalf("code %q is not XXXXX-XXXXX", code)
	}
	if NormalizeCode(code) != code {
		t.Fatalf("a generated code must normalize to itself")
	}
	if NormalizeCode("abcde fghjk") != "ABCDE-FGHJK" {
		t.Fatal("entry ignores case and spaces")
	}
	if NormalizeCode("ILOU1-2345U") != "" {
		t.Fatal("U is not in the code alphabet")
	}
	if NormalizeCode("iLO12-34567") != "11012-34567" {
		t.Fatal("I and L read as 1, O reads as 0")
	}
}
