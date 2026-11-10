package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Question statuses in the read model.
const (
	QuestionPending      = "pending"
	QuestionAnswered     = "answered"
	QuestionClosedStatus = "closed"
)

// maxQuestions bounds the question list shipped by thread.get. Only closed
// questions are dropped to make room: a pending one always has a blocked caller.
const maxQuestions = 100

// Question is one agent question in the read model: the asked payload plus how
// it closed.
type Question struct {
	contracts.QuestionAsked
	Status   string    `json:"status"`
	Selected []string  `json:"selected,omitempty"`
	Text     string    `json:"text,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at"`
}

// question returns the question with id, or nil. The pointer is into the
// state, so callers in the projector may update it in place.
func (s *ThreadState) question(id string) *Question {
	for i := range s.Questions {
		if s.Questions[i].QuestionID == id {
			return &s.Questions[i]
		}
	}
	return nil
}

func (s *ThreadState) pendingQuestions() int {
	n := 0
	for i := range s.Questions {
		if s.Questions[i].Status == QuestionPending {
			n++
		}
	}
	return n
}

// trimQuestions drops the oldest closed questions past the cap.
func (s *ThreadState) trimQuestions() {
	overflow := len(s.Questions) - maxQuestions
	if overflow <= 0 {
		return
	}
	kept := make([]Question, 0, maxQuestions)
	for _, q := range s.Questions {
		if overflow > 0 && q.Status != QuestionPending {
			overflow--
			continue
		}
		kept = append(kept, q)
	}
	s.Questions = kept
}

// normalizeOptions checks a question's options against its kind and returns them
// trimmed. A text question takes none.
func normalizeOptions(kind contracts.QuestionKind, raw []string) ([]string, error) {
	if kind == contracts.QuestionText {
		if len(raw) > 0 {
			return nil, fmt.Errorf("%w: a text question takes no options", ErrInvalidCommand)
		}
		return nil, nil
	}
	if len(raw) < 2 || len(raw) > contracts.MaxQuestionOptions {
		return nil, fmt.Errorf("%w: a %s question needs 2 to %d options", ErrInvalidCommand, kind, contracts.MaxQuestionOptions)
	}
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, o := range raw {
		o = strings.TrimSpace(o)
		if o == "" || len(o) > contracts.MaxQuestionOptionLength {
			return nil, fmt.Errorf("%w: each option must be 1 to %d characters", ErrInvalidCommand, contracts.MaxQuestionOptionLength)
		}
		if seen[o] {
			return nil, fmt.Errorf("%w: option %q is listed twice", ErrInvalidCommand, o)
		}
		seen[o] = true
		out = append(out, o)
	}
	return out, nil
}

// validateAnswer checks an answer against the question it closes. A choice takes
// one option or an "other" text, never both; a multi takes any options and may
// add text; a text takes text only.
func validateAnswer(q contracts.QuestionAsked, selected []string, text string) ([]string, string, error) {
	text = strings.TrimSpace(text)
	if len(text) > contracts.MaxQuestionLength {
		return nil, "", fmt.Errorf("%w: answer text is too long", ErrInvalidCommand)
	}
	known := make(map[string]bool, len(q.Options))
	for _, o := range q.Options {
		known[o] = true
	}
	chosen := make(map[string]bool, len(selected))
	for _, s := range selected {
		if !known[s] {
			return nil, "", fmt.Errorf("%w: %q is not an option of this question", ErrInvalidCommand, s)
		}
		if chosen[s] {
			return nil, "", fmt.Errorf("%w: option %q is selected twice", ErrInvalidCommand, s)
		}
		chosen[s] = true
	}
	switch q.Kind {
	case contracts.QuestionText:
		if len(selected) > 0 || text == "" {
			return nil, "", fmt.Errorf("%w: a text question takes text as its answer", ErrInvalidCommand)
		}
	case contracts.QuestionChoice:
		if (len(selected) == 1) == (text != "") {
			return nil, "", fmt.Errorf("%w: a choice takes one option, or other text when no option fits", ErrInvalidCommand)
		}
		if len(selected) > 1 {
			return nil, "", fmt.Errorf("%w: a choice takes one option", ErrInvalidCommand)
		}
	case contracts.QuestionMulti:
		if len(selected) == 0 && text == "" {
			return nil, "", fmt.Errorf("%w: pick at least one option or give other text", ErrInvalidCommand)
		}
	default:
		return nil, "", fmt.Errorf("%w: unknown question kind %q", ErrInvalidCommand, q.Kind)
	}
	return selected, text, nil
}
