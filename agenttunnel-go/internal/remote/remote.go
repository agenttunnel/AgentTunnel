// Package remote connects the daemon to a messaging surface so workflow
// updates reach the user away from the desktop. Today that surface is WhatsApp:
// a bridge pairs a device, persists that choice under StateDir, and forwards
// turn results, agent failures, and questions from the engine event stream.
//
// The same chat is also a control surface when the user turns Control on: text
// the linked account sends to itself is routed through a strict command
// whitelist (see internal/transport), and the orchestrator's final summary comes
// back through the normal update path.
//
// The WhatsApp client itself sits behind Adapter: the shipped adapter links
// this daemon as a WhatsApp companion device (see linked.go), and the Cloud API
// adapter (see cloud.go) remains for deployments that already run a Business
// number. Either way the bridge, its persistence, and the RPC surface are
// unchanged.
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
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
)

// State is one connectivity state a Remote item can report.
type State string

const (
	StateOff        State = "off"
	StateConnecting State = "connecting"
	StatePairing    State = "pairing"
	StateConnected  State = "connected"
	StateError      State = "error"
)

// Adapter is the WhatsApp client the bridge drives. Connect starts pairing; it
// calls onQR with a linked-device QR to display (possibly never, for adapters
// that pair another way) and onLinked once messages can be sent. Connect may
// return before linking finishes, in which case the callbacks report progress.
// An adapter that fails later (an expired QR, or the phone unlinking this
// device) reports it through onError so the item stops claiming to be paired or
// connected. Send delivers one text message; Disconnect unlinks the device.
type Adapter interface {
	Connect(ctx context.Context, onQR func(qr string), onLinked func(detail string), onError func(error)) error
	Disconnect() error
	Send(ctx context.Context, text string) error
}

// InboundMessage is one text the user typed in the bridge's own chat: the
// linked account talking to itself. ID is WhatsApp's message id, which the
// bridge uses to drop duplicates and the echo of its own updates.
type InboundMessage struct {
	ID       string
	Text     string
	FromSelf bool
}

// InboundAdapter is an Adapter that can also receive messages. SetInbound
// installs the handler the bridge passes in before Connect; the adapter calls it
// for text messages in the linked account's self-chat only. SendTracked reports
// the delivered message id, so the bridge can recognise WhatsApp's echo of its
// own outbound update.
type InboundAdapter interface {
	Adapter
	SetInbound(handler func(InboundMessage))
	SendTracked(ctx context.Context, text string) (id string, err error)
}

// adapterCloser is an Adapter that can stop its socket without unlinking the
// device. The bridge calls it when the daemon shuts down, so a linked session
// survives a restart; the user's Disconnect still unlinks.
type adapterCloser interface {
	Close()
}

// Options configure a bridge.
type Options struct {
	// StateDir holds whatsapp.json: whether the user wants WhatsApp updates.
	StateDir string
	// Engine is the event source for workflow updates. Nil disables them.
	Engine *domain.Engine
	// Adapter is the WhatsApp client. Nil reports the feature unavailable.
	Adapter Adapter
	// Control handles one message the user typed in the bridge's own chat and
	// returns the reply to send back. It is the daemon's command router. Nil
	// disables inbound control entirely.
	Control func(ctx context.Context, text string) string
	// OnChange fires whenever the item's state changes, outside the bridge lock.
	OnChange func()
	Logf     func(format string, args ...any)
}

// Bridge pairs a WhatsApp device and forwards workflow updates to it. It
// implements transport.RemoteControl.
type Bridge struct {
	opts Options
	path string
	logf func(string, ...any)

	mu     sync.Mutex
	state  State
	detail string
	qr     string
	// gen invalidates adapter callbacks from a connect attempt that has been
	// superseded by a disconnect or a newer connect.
	gen    int
	unsub  func()
	cancel context.CancelFunc
	closed bool
	// enabled is the persisted notification choice. Control is the separate,
	// off-by-default setting that lets the user's own chat messages drive the
	// orchestrator; the two are independent.
	enabled bool
	control bool
	// runCtx is the context in Start, so inbound control work is cancelled with
	// the bridge.
	runCtx context.Context
	// out decouples the event reader from the network: a slow send must never
	// let high-volume agent output crowd turn updates out of the event channel.
	out chan string
	// lifeCtx is the bridge's own lifetime, cancelled by Close. The inbound
	// worker runs on it, so a chat command can never outlive the bridge even
	// when Start was never called.
	lifeCtx    context.Context
	lifeCancel context.CancelFunc
	// in is the bounded FIFO of accepted chat commands, drained by exactly one
	// worker. Serial routing is what makes /use 2 followed by plain text act on
	// the selected thread; a burst cannot spawn unbounded goroutines.
	in chan string

	// now stamps echo text so the text fallback expires instead of hiding a
	// later legitimate command that repeats an earlier update.
	now func() time.Time

	// inMu guards the echo and dedupe windows. Echo suppression and dedupe are
	// separate: an update the bridge sent comes back as a message event, and the
	// same inbound message may be delivered more than once.
	inMu      sync.Mutex
	sentIDs   []string
	sentTexts []echoText
	seenIDs   []string
}

// echoText is one outbound text the bridge remembers to recognise its echo.
// The timestamp bounds how long the less reliable text match applies.
type echoText struct {
	text string
	at   time.Time
}

// storedState is whatsapp.json: the user's choice to receive updates, and the
// separate Control choice, kept across daemon restarts so a paired device
// reconnects on start and the control setting survives.
type storedState struct {
	Enabled bool `json:"enabled"`
	// Control lets the user's own self-chat messages drive the orchestrator.
	// Absent means off, so an old whatsapp.json stays read-only.
	Control bool `json:"control,omitempty"`
}

// New builds a bridge. It does not connect; call Start (daemon) or Connect.
func New(opts Options) (*Bridge, error) {
	if opts.StateDir == "" {
		return nil, errors.New("remote: StateDir is required")
	}
	b := &Bridge{
		opts:   opts,
		path:   filepath.Join(opts.StateDir, "whatsapp.json"),
		logf:   func(string, ...any) {},
		state:  StateOff,
		detail: "Not connected",
		out:    make(chan string, updateQueue),
		in:     make(chan string, inboundQueue),
		now:    time.Now,
	}
	if opts.Logf != nil {
		b.logf = opts.Logf
	}
	b.lifeCtx, b.lifeCancel = context.WithCancel(context.Background())
	if st, ok := b.load(); ok {
		b.enabled = st.Enabled
		b.control = st.Control
	}
	// Only an adapter that can receive messages gets a handler, and only when
	// the daemon has somewhere to route them.
	if control := opts.Control; control != nil {
		if in, ok := opts.Adapter.(InboundAdapter); ok {
			in.SetInbound(b.handleInbound)
		}
	}
	// One worker drains accepted commands in arrival order. It is started here,
	// not in Start, so ordering does not depend on the caller remembering to
	// start notifications first.
	go b.workInbound(b.lifeCtx)
	return b, nil
}

// Start subscribes the bridge to engine events and reconnects a device the user
// left enabled. It returns at once; updates are delivered in the background.
func (b *Bridge) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.cancel = cancel
	b.runCtx = runCtx
	enabled := b.enabled
	b.mu.Unlock()
	if b.opts.Engine != nil {
		events, unsub := b.opts.Engine.SubscribeAll()
		b.mu.Lock()
		b.unsub = unsub
		b.mu.Unlock()
		go b.forward(runCtx, events)
	}
	go b.deliver(runCtx)
	if enabled {
		go func() {
			if err := b.Connect(runCtx); err != nil {
				b.logf("whatsapp: reconnect: %v", err)
			}
		}()
	}
}

// Close stops forwarding updates and unlinks the device. The enabled choice is
// kept, so the next daemon start reconnects it. It is idempotent: the daemon
// and a caller may both close the bridge.
func (b *Bridge) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	unsub := b.unsub
	b.unsub = nil
	cancel := b.cancel
	b.cancel = nil
	lifeCancel := b.lifeCancel
	b.lifeCancel = nil
	b.closed = true
	b.gen++
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if lifeCancel != nil {
		lifeCancel()
	}
	if unsub != nil {
		unsub()
	}
	if b.opts.Adapter != nil {
		if closer, ok := b.opts.Adapter.(adapterCloser); ok {
			closer.Close()
		} else {
			_ = b.opts.Adapter.Disconnect()
		}
	}
}

// Status reports the WhatsApp item for the Remote section.
func (b *Bridge) Status() transport.RemoteItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.itemLocked()
}

func (b *Bridge) itemLocked() transport.RemoteItem {
	state := b.state
	if state == "" {
		state = StateOff
	}
	item := transport.RemoteItem{
		ID:      "whatsapp",
		Label:   "WhatsApp",
		State:   string(state),
		Detail:  b.detail,
		QR:      b.qr,
		Control: b.control,
	}
	_, inbound := b.opts.Adapter.(InboundAdapter)
	item.ControlSupported = inbound && b.opts.Control != nil
	if b.opts.Adapter == nil {
		// A build without a configured adapter has no way to connect, so the
		// section must not offer a working-looking toggle.
		item.Unavailable = true
		if state == StateOff {
			item.Detail = unavailableDetail
		}
	}
	return item
}

// Connect starts pairing, or returns the current item when already pairing or
// connected. It is idempotent, so a second click cannot start two linkers.
func (b *Bridge) Connect(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("whatsapp: bridge is closed")
	}
	if b.state == StateConnecting || b.state == StatePairing || b.state == StateConnected {
		b.mu.Unlock()
		return nil
	}
	if b.opts.Adapter == nil {
		b.state = StateError
		b.detail = unavailableDetail
		b.mu.Unlock()
		b.changed()
		return errors.New("whatsapp is not configured: " + unavailableDetail)
	}
	b.gen++
	gen := b.gen
	b.state = StateConnecting
	b.detail = "Starting WhatsApp"
	b.qr = ""
	b.enabled = true
	b.mu.Unlock()

	b.saveState()
	b.changed()

	err := b.opts.Adapter.Connect(ctx,
		func(qr string) { b.paired(gen, qr) },
		func(detail string) { b.linked(gen, detail) },
		func(err error) { b.failed(gen, err) },
	)
	if err != nil {
		b.failed(gen, err)
		return err
	}
	return nil
}

// Disconnect unlinks the device and turns updates off. The enabled choice is
// persisted, so a later Connect (or daemon start) is enough to come back.
func (b *Bridge) Disconnect() error {
	b.mu.Lock()
	b.gen++
	b.state = StateOff
	b.detail = "Not connected"
	b.qr = ""
	b.enabled = false
	adapter := b.opts.Adapter
	b.mu.Unlock()

	b.saveState()
	b.changed()
	if adapter == nil {
		return nil
	}
	return adapter.Disconnect()
}

// paired records the QR a linked-device adapter produced.
func (b *Bridge) paired(gen int, qr string) {
	b.mu.Lock()
	if b.gen != gen {
		b.mu.Unlock()
		return
	}
	b.state = StatePairing
	b.qr = qr
	b.detail = "Scan the QR with WhatsApp > Linked devices"
	b.mu.Unlock()
	b.changed()
}

// linked records a completed link. The detail is the adapter's, so a Cloud API
// adapter can name the verified number.
func (b *Bridge) linked(gen int, detail string) {
	b.mu.Lock()
	if b.gen != gen {
		b.mu.Unlock()
		return
	}
	b.state = StateConnected
	b.qr = ""
	if strings.TrimSpace(detail) == "" {
		detail = "Connected"
	}
	b.detail = detail
	b.enabled = true
	b.mu.Unlock()
	b.saveState()
	b.changed()
}

// Control reports the persisted inbound-control setting. It is independent of
// whether the device is linked and of the notification choice.
func (b *Bridge) Control() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.control
}

// SetControl turns inbound control on or off and persists it. Turning it on
// grants anyone holding the linked account's self-chat the ability to drive the
// selected thread's orchestrator, so the caller is the desktop app, never a
// relayed phone.
func (b *Bridge) SetControl(enabled bool) error {
	b.mu.Lock()
	b.control = enabled
	b.mu.Unlock()
	b.saveState()
	b.changed()
	return nil
}

// failed records a connect error. It does not clear the enabled choice: the
// user asked for updates, and the next daemon start retries.
func (b *Bridge) failed(gen int, err error) {
	b.mu.Lock()
	if b.gen != gen {
		b.mu.Unlock()
		return
	}
	b.state = StateError
	b.detail = err.Error()
	b.qr = ""
	b.mu.Unlock()
	b.changed()
}

func (b *Bridge) changed() {
	b.mu.Lock()
	notify := b.opts.OnChange
	b.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// forward turns engine events into WhatsApp messages. It only enqueues, so the
// event channel keeps draining even while a send is in flight.
func (b *Bridge) forward(ctx context.Context, events <-chan contracts.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if text := b.updateFor(ev); text != "" {
				b.enqueue(text)
			}
		}
	}
}

// enqueue hands one update to the sender. A full queue drops the update rather
// than blocking the event reader; the bound covers a burst of failures.
func (b *Bridge) enqueue(text string) {
	select {
	case b.out <- text:
	default:
		b.logf("whatsapp: update queue full, dropping update")
	}
}

// deliver sends queued updates one at a time.
func (b *Bridge) deliver(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-b.out:
			b.send(ctx, text)
		}
	}
}

// updateFor is the bridge's whole notification policy: a turn's final summary,
// a periodic workflow status report, an agent failure, and a question for the
// human. It runs for every engine event, including each streamed agent line, so
// it stays cheap: no thread state is read unless the event is one of the four
// updates, and none at all while the device is unlinked.
func (b *Bridge) updateFor(ev contracts.Event) string {
	b.mu.Lock()
	connected := b.state == StateConnected
	b.mu.Unlock()
	if !connected {
		return ""
	}
	switch p := ev.Payload.(type) {
	case contracts.MessageSent:
		if strings.TrimSpace(p.Text) == "" {
			return ""
		}
		switch p.Author {
		case "orchestrator":
			// The orchestrator's final summary is the only message an agent writes
			// on its own behalf at the end of a turn.
			return fmt.Sprintf("%s — turn complete\n\n%s", b.threadTitle(ev.ThreadID), clip(p.Text, updateMax))
		case contracts.AuthorStatus:
			// The report already opens with its own header, so only the thread
			// title is added, to say which thread it came from. It is sent as text, never parsed as a command.
			return fmt.Sprintf("%s — status update\n\n%s", b.threadTitle(ev.ThreadID), clip(p.Text, updateMax))
		}
		return ""
	case contracts.AgentStatusChanged:
		if p.Status != contracts.AgentFailed {
			return ""
		}
		reason := strings.TrimSpace(p.Reason)
		if reason == "" {
			reason = "no reason given"
		}
		return fmt.Sprintf("%s — agent %s failed: %s", b.threadTitle(ev.ThreadID), p.AgentID, clip(reason, updateMax))
	case contracts.QuestionAsked:
		text := fmt.Sprintf("%s — %s asks: %s", b.threadTitle(ev.ThreadID), p.AgentID, clip(p.Question, updateMax))
		if len(p.Options) > 0 {
			text += "\n\n" + strings.Join(p.Options, "\n")
		}
		return text
	}
	return ""
}

// send delivers one update when the device is linked. The text is remembered
// before the send, so an echo that races the reply is still recognised, and an
// adapter that reports message ids lets the echo be matched by id as well.
func (b *Bridge) send(ctx context.Context, text string) {
	b.mu.Lock()
	connected := b.state == StateConnected
	adapter := b.opts.Adapter
	b.mu.Unlock()
	if !connected || adapter == nil {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	b.recordSent("", text)
	if in, ok := adapter.(InboundAdapter); ok {
		id, err := in.SendTracked(sendCtx, text)
		if err != nil {
			b.logf("whatsapp: send: %v", err)
			return
		}
		b.recordSent(id, "")
		return
	}
	if err := adapter.Send(sendCtx, text); err != nil {
		b.logf("whatsapp: send: %v", err)
	}
}

// handleInbound is the adapter's delivery callback. The decision whether a
// message counts happens synchronously, so a dropped message starts no work;
// accepted commands go to the bridge's single worker, which routes them in
// arrival order. A full queue drops the command with a notice rather than
// spawning work outside that order.
func (b *Bridge) handleInbound(msg InboundMessage) {
	text, ok := b.acceptInbound(msg)
	if !ok {
		return
	}
	if b.opts.Control == nil {
		return
	}
	select {
	case b.in <- text:
	default:
		b.logf("whatsapp: inbound queue full, dropping command")
		b.enqueue(inboundQueueFull)
	}
}

// workInbound drains accepted commands one at a time. It is the only caller of
// the control handler, so "/use 2" always commits before a following message
// reads the selection.
func (b *Bridge) workInbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-b.in:
			b.routeInbound(b.controlContext(), text)
		}
	}
}

// routeInbound runs one command and queues its reply. It is deliberately not
// concurrent: a turn start would otherwise race the selection the previous
// message set.
func (b *Bridge) routeInbound(ctx context.Context, text string) {
	handler := b.opts.Control
	if handler == nil {
		return
	}
	if reply := handler(ctx, text); strings.TrimSpace(reply) != "" {
		b.enqueue(reply)
	}
}

// acceptInbound is the whole inbound policy: only the linked account's own
// chat, only while control is on and the device is linked, no echo of the
// bridge's own update, and each message id at most once.
func (b *Bridge) acceptInbound(msg InboundMessage) (string, bool) {
	if !msg.FromSelf {
		return "", false
	}
	b.mu.Lock()
	connected, control := b.state == StateConnected, b.control
	b.mu.Unlock()
	if !connected || !control {
		return "", false
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return "", false
	}

	b.inMu.Lock()
	defer b.inMu.Unlock()
	if msg.ID != "" {
		if contains(b.sentIDs, msg.ID) || contains(b.seenIDs, msg.ID) {
			return "", false
		}
		b.seenIDs = appendBounded(b.seenIDs, msg.ID, dedupeWindow)
	}
	// The id is the reliable signal; text only covers the moment before the
	// send's id is known, so it must not keep hiding a command that happens to
	// repeat an older update.
	now := b.now()
	for _, sent := range b.sentTexts {
		if sent.text == text && now.Sub(sent.at) < echoTextWindow {
			return "", false
		}
	}
	return text, true
}

// recordSent remembers an outbound message id and text so WhatsApp's echo of it
// is dropped on the way back.
func (b *Bridge) recordSent(id, text string) {
	b.inMu.Lock()
	defer b.inMu.Unlock()
	if text = strings.TrimSpace(text); text != "" {
		b.sentTexts = appendBounded(b.sentTexts, echoText{text: text, at: b.now()}, echoWindow)
	}
	if id != "" {
		b.sentIDs = appendBounded(b.sentIDs, id, echoWindow)
	}
}

func (b *Bridge) controlContext() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runCtx != nil {
		return b.runCtx
	}
	return b.lifeCtx
}

// appendBounded adds value to a FIFO window, dropping the oldest entries past
// max. Echo and dedupe only need to cover a burst, not the whole session.
func appendBounded[T any](list []T, value T, max int) []T {
	list = append(list, value)
	if len(list) > max {
		list = list[len(list)-max:]
	}
	return list
}

func contains[T comparable](list []T, value T) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func (b *Bridge) threadTitle(id contracts.ThreadID) string {
	if b.opts.Engine == nil {
		return string(id)
	}
	st, err := b.opts.Engine.State(context.Background(), id)
	if err != nil || strings.TrimSpace(st.Title) == "" {
		return string(id)
	}
	return st.Title
}

// load reads whatsapp.json. A missing or unreadable file is simply no saved
// choice: WhatsApp stays off rather than failing the daemon.
func (b *Bridge) load() (storedState, bool) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return storedState{}, false
	}
	var st storedState
	if json.Unmarshal(data, &st) != nil {
		return storedState{}, false
	}
	return st, true
}

// saveState writes the current choices. The snapshot and the write both happen
// under the lock, so two concurrent changes cannot rename stale state last and
// leave the file disagreeing with memory. A failure is logged, not fatal: this
// run's in-memory state stays correct.
func (b *Bridge) saveState() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.persist(storedState{Enabled: b.enabled, Control: b.control})
}

// persist writes whatsapp.json.
func (b *Bridge) persist(st storedState) {
	data, err := json.Marshal(st)
	if err != nil {
		b.logf("whatsapp: encode state: %v", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(b.path), ".whatsapp-*.json")
	if err != nil {
		b.logf("whatsapp: save state: %v", err)
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		b.logf("whatsapp: save state: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		b.logf("whatsapp: save state: %v", err)
		return
	}
	if err := os.Rename(name, b.path); err != nil {
		os.Remove(name)
		b.logf("whatsapp: save state: %v", err)
	}
}

// unavailableDetail explains a build without an adapter. The row stays off and
// the UI must not present it as a toggle that can work.
const unavailableDetail = "Not configured: this build has no WhatsApp adapter"

// updateMax bounds one WhatsApp message. A long turn summary stays readable and
// fits comfortably in a single text message.
const updateMax = 1200

// updateQueue bounds updates waiting on a slow network. Agent output never
// enters it, so this only holds real workflow updates.
const updateQueue = 32

// inboundQueue bounds chat commands waiting on the single worker. Commands are
// short (a turn start only claims the slot), so the bound covers a fast burst;
// past it the bridge drops with a notice rather than breaking arrival order.
const inboundQueue = 32

// inboundQueueFull is the one notice a chat gets when its command is dropped.
const inboundQueueFull = "Too many commands in a row; wait a moment and send that again."

// sendTimeout bounds one adapter send so a stalled network cannot wedge the
// sender.
const sendTimeout = 15 * time.Second

// echoWindow bounds the recent outbound ids and texts remembered to recognise
// WhatsApp's echo of the bridge's own updates. A chat burst, not the session.
const echoWindow = 64

// echoTextWindow bounds the text fallback. The recorded id is the reliable
// signal; text only bridges the instant between recording the send and learning
// its id, so keeping it short avoids dropping a legitimate command that repeats
// a recent update.
const echoTextWindow = 30 * time.Second

// dedupeWindow bounds the inbound message ids seen, so a message delivered
// twice drives the orchestrator once.
const dedupeWindow = 128

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
