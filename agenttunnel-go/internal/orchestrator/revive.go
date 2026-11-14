package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// reviveTask converts one lead revive call into a fresh run of a finished
// subagent, so the lead can ask a follow-up the result summary did not cover.
// The revived task keeps the target's name and role — a repeated name is
// legal, the latest row wins — and carries a fresh agent id, so it runs as its
// own agent under the same identity. Its task text hands the new harness run
// its own context back: the original task, the agent's prior output log (the
// same rendering agent-logs serves), and the lead's question.
func (o *Orchestrator) reviveTask(ctx context.Context, threadID contracts.ThreadID, leadID contracts.AgentID, call toolbox.Call) (contracts.DispatchTask, bool) {
	// A refused revive is recorded for the lead the same way a failed peer
	// message is, so the model behind it sees why nothing was re-dispatched.
	refuse := func(line string) {
		_ = o.runner.logs.Append(threadID, contracts.LogEntry{AgentID: leadID, Kind: contracts.LogError, Text: line, At: o.runner.clock()})
		o.runner.publishOutput(threadID, leadID, contracts.LogError, line)
	}
	to := strings.TrimSpace(call.To)
	if to == "" {
		refuse("tool revive failed: --to is required")
		return contracts.DispatchTask{}, false
	}
	question := strings.TrimSpace(call.Text)
	if question == "" {
		refuse("tool revive failed: --text is required")
		return contracts.DispatchTask{}, false
	}
	st, err := o.runner.engine.State(ctx, threadID)
	if err != nil {
		refuse("tool revive failed: " + err.Error())
		return contracts.DispatchTask{}, false
	}
	targetID, ok := ResolveLogTarget(st, to)
	if !ok {
		refuse(fmt.Sprintf("tool revive failed: unknown agent %s", to))
		return contracts.DispatchTask{}, false
	}
	target := st.Agents[targetID]
	if !target.Status.Terminal() {
		refuse(fmt.Sprintf("tool revive refused: %s (%s) is still running; to reach it now, send it a message instead: \"$AT_TOOL\" tool message --to %s --text \"...\"",
			target.Name, targetID, target.Name))
		return contracts.DispatchTask{}, false
	}
	original := strings.TrimSpace(target.Task)
	logText, _ := RenderAgentLog(o.runner.logs.AgentEntries(threadID, targetID))
	// Agent ids must be unique per dispatch, so the revived run takes a fresh
	// sequence even though it keeps the target's roster name. A target whose
	// id was not minted by this counter (a hand-written test id, a protocol
	// agent) can already own the next value, so bump past anything recorded.
	id := contracts.AgentID(fmt.Sprintf("agent-%s-%d", target.Name, o.seq.Add(1)))
	for tries := 0; tries < 100; tries++ {
		if _, exists := st.Agents[id]; !exists {
			break
		}
		id = contracts.AgentID(fmt.Sprintf("agent-%s-%d", target.Name, o.seq.Add(1)))
	}
	if _, exists := st.Agents[id]; exists {
		refuse("tool revive failed: no free agent id")
		return contracts.DispatchTask{}, false
	}
	task := contracts.DispatchTask{
		AgentID:       id,
		ParentAgentID: leadID,
		Name:          target.Name,
		Role:          target.Role,
		Task:          revivePrompt(target.Name, original, logText, question),
	}
	o.runner.auditLog("revive.dispatched", map[string]any{
		"threadId": string(threadID), "target": string(targetID),
		"name": target.Name, "role": string(target.Role),
	})
	return task, true
}

// revivePrompt builds the revived run's task: a preamble saying who the agent
// is, its original task, its previous run's output log, and the lead's
// question. The log rendering comes from RenderAgentLog, ending in a newline,
// so sections stay readable whatever it carried.
func revivePrompt(name, originalTask, log, question string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are subagent %s. You previously completed a task in this thread, and the team lead has a follow-up question about your result.\n\n## Your original task\n", name)
	if originalTask != "" {
		b.WriteString(originalTask)
	} else {
		b.WriteString("(the original task text is not recorded in this thread)")
	}
	if strings.TrimSpace(log) != "" {
		b.WriteString("\n\n## Your previous run's output log\n")
		b.WriteString(log)
	}
	b.WriteString("\n## The team lead's question\n")
	b.WriteString(question)
	b.WriteString("\n\nAnswer the question based on your earlier work.")
	return b.String()
}
