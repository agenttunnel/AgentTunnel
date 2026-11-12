// Package steering queues messages the user sends while a turn is already
// running. The orchestrator drains the queue before each pass and weighs the
// steering, instead of the message starting a competing turn.
package steering

import (
	"strings"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// Entry is one queued steering message: the prompt text handed to the next
// pass, the transcript message it was recorded as, plus any images the user
// attached mid-run. The images ride the next pass the same way a turn's do.
type Entry struct {
	MessageID string
	Text      string
	Images    []providers.ImageInput
}

// Box holds pending steering messages, partitioned by thread.
type Box struct {
	mu      sync.Mutex
	pending map[contracts.ThreadID][]Entry
}

// New returns an empty queue.
func New() *Box {
	return &Box{pending: map[contracts.ThreadID][]Entry{}}
}

// Post records a steering message for a thread.
func (b *Box) Post(threadID contracts.ThreadID, messageID, text string, images ...providers.ImageInput) {
	text = strings.TrimSpace(text)
	if threadID == "" || (text == "" && len(images) == 0) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending[threadID] = append(b.pending[threadID], Entry{MessageID: messageID, Text: text, Images: images})
}

// Drain returns and clears a thread's pending steering messages.
func (b *Box) Drain(threadID contracts.ThreadID) []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := b.pending[threadID]
	delete(b.pending, threadID)
	return entries
}

// Has reports whether a thread has pending steering without consuming it.
func (b *Box) Has(threadID contracts.ThreadID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending[threadID]) > 0
}
