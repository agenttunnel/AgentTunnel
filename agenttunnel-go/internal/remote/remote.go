// Package remote connects the daemon to a messaging surface so workflow
// updates reach the user away from the desktop. Today that surface is WhatsApp:
// a bridge pairs a device, persists that choice under StateDir, and forwards
// turn results, agent failures, and questions from the engine event stream.
//
// The WhatsApp client itself sits behind Adapter: the shipped adapter talks to
// the WhatsApp Cloud API (see cloud.go), and a linked-device client can replace
// it without touching the bridge, its persistence, or the RPC surface.
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
// Send delivers one text message; Disconnect unlinks the device.
type Adapter interface {
	Connect(ctx context.Context, onQR func(qr string), onLinked func(detail string)) error
	Disconnect() error
	Send(ctx context.Context, text string) error
}

// Options configure a bridge.
type Options struct {
	// StateDir holds whatsapp.json: whether the user wants WhatsApp updates.
	StateDir string
	// Engine is the event source for workflow updates. Nil disables them.
	Engine *domain.Engine
	// Adapter is the WhatsApp client. Nil reports the feature unavailable.
	Adapter Adapter
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
	// out decouples the event reader from the network: a slow send must never
	// let high-volume agent output crowd turn updates out of the event channel.
	out chan string
}

// storedState is whatsapp.json: the user's choice to receive updates, kept
// across daemon restarts so a paired device reconnects on start.
type storedState struct {
	Enabled bool `json:"enabled"`
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
	}
	if opts.Logf != nil {
		b.logf = opts.Logf
	}
	return b, nil
}

// Start subscribes the bridge to engine events and reconnects a device the user
// left enabled. It returns at once; updates are delivered in the background.
func (b *Bridge) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.cancel = cancel
	b.mu.Unlock()
	if b.opts.Engine != nil {
		events, unsub := b.opts.Engine.SubscribeAll()
		b.mu.Lock()
		b.unsub = unsub
		b.mu.Unlock()
		go b.forward(runCtx, events)
	}
	go b.deliver(runCtx)
	st, ok := b.load()
	if ok && st.Enabled {
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
	b.closed = true
	b.gen++
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if unsub != nil {
		unsub()
	}
	if b.opts.Adapter != nil {
		_ = b.opts.Adapter.Disconnect()
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
		ID:     "whatsapp",
		Label:  "WhatsApp",
		State:  string(state),
		Detail: b.detail,
		QR:     b.qr,
	}
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
	b.mu.Unlock()

	b.persist(storedState{Enabled: true})
	b.changed()

	err := b.opts.Adapter.Connect(ctx,
		func(qr string) { b.paired(gen, qr) },
		func(detail string) { b.linked(gen, detail) },
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
	adapter := b.opts.Adapter
	b.mu.Unlock()

	b.persist(storedState{Enabled: false})
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
	b.mu.Unlock()
	b.persist(storedState{Enabled: true})
	b.changed()
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
// an agent failure, and a question for the human. It runs for every engine
// event, including each streamed agent line, so it stays cheap: no thread state
// is read unless the event is one of the three updates, and none at all while
// the device is unlinked.
func (b *Bridge) updateFor(ev contracts.Event) string {
	b.mu.Lock()
	connected := b.state == StateConnected
	b.mu.Unlock()
	if !connected {
		return ""
	}
	switch p := ev.Payload.(type) {
	case contracts.MessageSent:
		// The orchestrator's final summary is the only message an agent writes
		// on its own behalf at the end of a turn.
		if p.Author != "orchestrator" || strings.TrimSpace(p.Text) == "" {
			return ""
		}
		return fmt.Sprintf("%s — turn complete\n\n%s", b.threadTitle(ev.ThreadID), clip(p.Text, updateMax))
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

// send delivers one update when the device is linked.
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
	if err := adapter.Send(sendCtx, text); err != nil {
		b.logf("whatsapp: send: %v", err)
	}
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

// persist writes the enabled choice. A failure is logged, not fatal: this run's
// in-memory state stays correct.
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

// unavailableDetail explains an unconfigured build. The row stays off and the
// UI must not present it as a toggle that can work.
const unavailableDetail = "Not configured: set AT_WHATSAPP_TOKEN, AT_WHATSAPP_PHONE_NUMBER_ID, and AT_WHATSAPP_TO"

// updateMax bounds one WhatsApp message. A long turn summary stays readable and
// fits comfortably in a single text message.
const updateMax = 1200

// updateQueue bounds updates waiting on a slow network. Agent output never
// enters it, so this only holds real workflow updates.
const updateQueue = 32

// sendTimeout bounds one adapter send so a stalled network cannot wedge the
// sender.
const sendTimeout = 15 * time.Second

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
