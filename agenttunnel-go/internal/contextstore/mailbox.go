package contextstore

import (
	"errors"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ErrMailboxDisabled is returned when posting while peer messaging is off.
var ErrMailboxDisabled = errors.New("peer mailbox is disabled")

// Mailbox is an opt-in peer-to-peer message bus between a thread's agents. It is
// disabled by default: every Post is rejected until SetEnabled(true).
//
// The enabled flag is not authoritative: production always enables the runtime
// box and gates on the durable ThreadState.PeerMailbox flag instead, so a
// restart cannot desync delivery from what the event log recorded. The flag
// remains so tests can exercise the error path.
type Mailbox struct {
	mu      sync.RWMutex
	enabled bool
	inboxes map[inboxKey][]contracts.MailboxPosted
}

// inboxKey partitions a runtime inbox by thread and agent. Agent ids repeat
// across threads — every thread's lead is the fixed agent-orchestrator — so
// keying by agent alone would let one thread's pass drain another thread's
// messages when turns run concurrently.
type inboxKey struct {
	thread contracts.ThreadID
	agent  contracts.AgentID
}

// NewMailbox returns a disabled mailbox.
func NewMailbox() *Mailbox {
	return &Mailbox{inboxes: map[inboxKey][]contracts.MailboxPosted{}}
}

// SetEnabled turns peer messaging on or off.
func (m *Mailbox) SetEnabled(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = v
}

// Enabled reports whether peer messaging is on.
func (m *Mailbox) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

// Post delivers a message to its recipient within the sender's thread, or
// fails if the mailbox is disabled.
func (m *Mailbox) Post(thread contracts.ThreadID, msg contracts.MailboxPosted) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		return ErrMailboxDisabled
	}
	m.inboxes[inboxKey{thread: thread, agent: msg.To}] = append(m.inboxes[inboxKey{thread: thread, agent: msg.To}], msg)
	return nil
}

// Inbox returns the messages addressed to an agent in a thread.
func (m *Mailbox) Inbox(thread contracts.ThreadID, agent contracts.AgentID) []contracts.MailboxPosted {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]contracts.MailboxPosted(nil), m.inboxes[inboxKey{thread: thread, agent: agent}]...)
}

// Drain returns the messages addressed to an agent in a thread and clears that
// inbox, so a prompt never re-delivers what a previous pass already saw.
func (m *Mailbox) Drain(thread contracts.ThreadID, agent contracts.AgentID) []contracts.MailboxPosted {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := inboxKey{thread: thread, agent: agent}
	msgs := m.inboxes[key]
	delete(m.inboxes, key)
	return msgs
}
