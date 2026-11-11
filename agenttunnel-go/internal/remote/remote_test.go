package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func (f *fakeAdapter) Send(_ context.Context, text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, text)
	ch := f.sentCh
	f.mu.Unlock()
	if ch != nil {
		ch <- text
	}
	return nil
}

func (f *fakeAdapter) sends() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
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
