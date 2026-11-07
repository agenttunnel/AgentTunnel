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
type Mailbox struct {
	mu      sync.RWMutex
	enabled bool
	inboxes map[contracts.AgentID][]contracts.MailboxPosted
}

// NewMailbox returns a disabled mailbox.
func NewMailbox() *Mailbox {
	return &Mailbox{inboxes: map[contracts.AgentID][]contracts.MailboxPosted{}}
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

// Post delivers a message to its recipient, or fails if the mailbox is disabled.
func (m *Mailbox) Post(msg contracts.MailboxPosted) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		return ErrMailboxDisabled
	}
	m.inboxes[msg.To] = append(m.inboxes[msg.To], msg)
	return nil
}

// Inbox returns the messages addressed to an agent.
func (m *Mailbox) Inbox(agent contracts.AgentID) []contracts.MailboxPosted {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]contracts.MailboxPosted(nil), m.inboxes[agent]...)
}

// Drain returns the messages addressed to an agent and clears its inbox, so a
// prompt never re-delivers what a previous pass already saw.
func (m *Mailbox) Drain(agent contracts.AgentID) []contracts.MailboxPosted {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.inboxes[agent]
	delete(m.inboxes, agent)
	return msgs
}
