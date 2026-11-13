package orchestrator

import (
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/files"
)

// ResolveLogTarget maps a tool --to value to the subagent it names: an exact
// agent id present in the thread wins, otherwise the newest agent carrying
// that roster name. Unlike a peer message, the latest run of a repeated name
// is what the caller is asking about whether it is running or terminal, so
// no live agent is preferred. The agent-logs tool and revive share this rule,
// so a name reads as the same agent through both.
func ResolveLogTarget(st *domain.ThreadState, to string) (contracts.AgentID, bool) {
	if _, ok := st.Agents[contracts.AgentID(to)]; ok {
		return contracts.AgentID(to), true
	}
	var latest contracts.AgentID
	for _, id := range st.AgentOrder {
		if st.Agents[id].Name == to {
			latest = id
		}
	}
	return latest, latest != ""
}

// agentLogMarker prefixes a non-text entry so a reader can tell prose from
// tool traffic and errors at a glance. Multi-line entry text is kept as-is.
func agentLogMarker(kind contracts.LogKind) string {
	switch kind {
	case contracts.LogText:
		return ""
	case contracts.LogTool:
		return "[tool] "
	case contracts.LogError:
		return "[error] "
	case contracts.LogReasoning:
		return "[reasoning] "
	default:
		return "[raw] "
	}
}

// RenderAgentLog renders one agent's entries oldest first, one entry per
// output line, each line terminated. The rendering is capped at
// files.MaxReadBytes, the same cap a file read carries: when it is over, whole
// leading lines are dropped so the newest lines stay — a lead asking for logs
// needs the tail — and the first line notes that older lines were truncated.
func RenderAgentLog(entries []contracts.LogEntry) (string, bool) {
	const notice = "(older lines truncated)"
	lines := make([]string, len(entries))
	size := 0
	for i, e := range entries {
		lines[i] = agentLogMarker(e.Kind) + e.Text
		size += len(lines[i]) + 1
	}
	if len(lines) == 0 {
		return "", false
	}
	if size <= files.MaxReadBytes {
		return strings.Join(lines, "\n") + "\n", false
	}
	for len(lines) > 0 && size+len(notice)+1 > files.MaxReadBytes {
		size -= len(lines[0]) + 1
		lines = lines[1:]
	}
	return notice + "\n" + strings.Join(lines, "\n") + "\n", true
}
