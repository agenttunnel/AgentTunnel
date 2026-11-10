package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// A /btw side question is a one-shot, read-only model call over a snapshot of
// the thread. Its answer is returned to the caller and never recorded: it writes
// no events or messages, takes no turn slot, and cannot interrupt the turn that
// may be running beside it.
const (
	btwTimeout        = 2 * time.Minute
	btwMaxQuestion    = 4000
	btwMaxMessages    = 20
	btwMessageClip    = 1500
	btwMessagesBudget = 12 << 10
	btwAgentClip      = 2 << 10
	btwAgentsBudget   = 8 << 10
)

// Btw answers a side question about a thread. The agent it runs under is fresh
// and carries no tool environment, so the model cannot dispatch work or message
// peers.
func (r *Runner) Btw(ctx context.Context, threadID contracts.ThreadID, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", errors.New("question is required")
	}
	if len(question) > btwMaxQuestion {
		question = clipHead(question, btwMaxQuestion)
	}
	st, err := r.engine.State(ctx, threadID)
	if err != nil {
		return "", err
	}
	if !st.Exists {
		return "", fmt.Errorf("thread %q does not exist", threadID)
	}

	if r.roles == nil || r.registry == nil {
		return "", errors.New("side questions are unavailable")
	}
	role, ok := r.roles.Get(contracts.RoleOrchestrator)
	if !ok {
		return "", errors.New("orchestrator role is missing")
	}
	harness, model, variant := r.roleRuntime(role)
	if !r.harnessAllowed(harness) {
		return "", fmt.Errorf("harness %s is disabled", harness)
	}

	ctx, cancel := context.WithTimeout(ctx, btwTimeout)
	defer cancel()

	agentID := contracts.AgentID("agent-btw-" + r.nextID())
	spec := providers.SessionSpec{
		AgentID:  agentID,
		ThreadID: threadID,
		Role:     role.ID,
		Harness:  harness,
		Model:    model,
		Variant:  variant,
		Prompt:   r.buildBtwPrompt(st, question),
		Workdir:  r.workdirFor(ctx, threadID),
	}

	adapter, err := r.registry.NewAdapter(spec)
	if err != nil {
		return "", err
	}
	if err := adapter.Start(ctx, spec); err != nil {
		return "", err
	}
	events, err := adapter.Stream(ctx)
	if err != nil {
		return "", err
	}

	var text []string
	for ev := range events {
		if ev.Usage != nil {
			// Spend is billed to the orchestrator, like title generation, so the
			// side agent does not appear as an agent in the usage views.
			r.recordUsage(ctx, threadID, "agent-orchestrator", role.ID, true, *ev.Usage)
		}
		if ev.Kind == contracts.LogText && strings.TrimSpace(ev.Text) != "" {
			text = append(text, ev.Text)
		}
	}
	_ = adapter.Wait(ctx)

	if err := ctx.Err(); err != nil {
		return "", err
	}
	answer := strings.TrimSpace(strings.Join(text, "\n"))
	if answer == "" {
		return "", errors.New("the side question got no answer")
	}
	return answer, nil
}

// buildBtwPrompt snapshots the thread: its title, roster, recent messages and
// each agent's recent output. The sections are clipped so a long thread keeps
// the prompt bounded.
func (r *Runner) buildBtwPrompt(st *domain.ThreadState, question string) string {
	var b strings.Builder
	b.WriteString("You are answering a side question about a thread while its agents keep working. ")
	b.WriteString("Answer from the thread context below. You cannot run tools, edit files, message agents, or dispatch work, so say so if the answer needs any of that. Be brief.\n\n")

	fmt.Fprintf(&b, "Thread: %s\n\n", st.Title)

	b.WriteString("Agents:\n")
	if roster := r.Roster(st); roster != "" {
		b.WriteString(roster)
	} else {
		b.WriteString("(none)\n")
	}

	b.WriteString("\nRecent messages:\n")
	msgs := st.Messages
	if len(msgs) > btwMaxMessages {
		msgs = msgs[len(msgs)-btwMaxMessages:]
	}
	var history strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&history, "[%s] %s\n", m.Author, clipHead(m.Text, btwMessageClip))
	}
	b.WriteString(clipTail(history.String(), btwMessagesBudget))

	if r.logs != nil {
		var output strings.Builder
		entries := r.logs.Entries(st.ID)
		for _, id := range st.AgentOrder {
			a := st.Agents[id]
			if a.IsOrchestrator() {
				continue
			}
			var text strings.Builder
			for _, e := range entries {
				if e.AgentID == id && e.Kind == contracts.LogText {
					text.WriteString(e.Text)
				}
			}
			if text.Len() == 0 {
				continue
			}
			fmt.Fprintf(&output, "## %s (%s)\n%s\n\n", a.Name, a.Status, clipTail(text.String(), btwAgentClip))
		}
		if output.Len() > 0 {
			b.WriteString("\nRecent agent output:\n")
			b.WriteString(clipTail(output.String(), btwAgentsBudget))
		}
	}

	b.WriteString("\nSide question:\n")
	b.WriteString(question)
	b.WriteString("\n")
	return b.String()
}

// clipHead keeps the first n bytes of s, backing off to a rune boundary.
func clipHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// clipTail keeps the last n bytes of s, backing off to a rune boundary.
func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return "…" + s[i:]
}
