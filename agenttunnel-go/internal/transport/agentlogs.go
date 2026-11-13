package transport

import (
	"net/http"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/files"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// handleAgentLogsCall serves agent-logs on behalf of a team lead: the full
// output log of one dispatched subagent, oldest first. A lead asks for it when
// a result summary is missing detail. Only a lead on this machine may call it;
// a remote peer's or a subagent's call is refused here, not queued, so the
// refusal reaches the caller.
func (s *Server) handleAgentLogsCall(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	if !isLoopbackPeer(r) {
		http.Error(w, "tool calls are accepted only from this machine", http.StatusForbidden)
		return
	}
	if !orchestrator.IsLead(req.AgentID) {
		http.Error(w, "only the orchestrator can read a subagent's logs", http.StatusForbidden)
		return
	}
	if s.runner == nil {
		http.Error(w, "agent logs are unavailable", http.StatusServiceUnavailable)
		return
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		http.Error(w, "to is required", http.StatusBadRequest)
		return
	}
	st, err := s.engine.State(r.Context(), req.ThreadID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !st.Exists {
		http.Error(w, "unknown thread "+string(req.ThreadID), http.StatusBadRequest)
		return
	}
	id, ok := resolveLogTarget(st, to)
	if !ok {
		http.Error(w, "unknown agent "+to, http.StatusBadRequest)
		return
	}
	name := st.Agents[id].Name
	entries := s.runner.Logs().AgentEntries(req.ThreadID, id)
	text, truncated := renderAgentLog(entries)
	s.envAudit(req, map[string]any{"target": string(id)})
	writeToolJSON(w, map[string]any{
		"ok": true, "agent": string(id), "name": name,
		"lines": len(entries), "truncated": truncated, "text": text,
	})
}

// resolveLogTarget maps a --to value to the subagent whose logs the lead
// wants: an exact agent id present in the thread wins, otherwise the newest
// agent carrying that roster name. Unlike a peer message, the latest run of a
// repeated name is what the lead is asking about whether it is running or
// terminal, so no live agent is preferred.
func resolveLogTarget(st *domain.ThreadState, to string) (contracts.AgentID, bool) {
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

// agentLogMarker prefixes a non-text entry so the lead can tell prose from
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

// renderAgentLog renders one agent's entries oldest first, one entry per
// output line, each line terminated. The rendering is capped at
// files.MaxReadBytes, the same cap a file read carries: when it is over, whole
// leading lines are dropped so the newest lines stay — a lead asking for logs
// needs the tail — and the first line notes that older lines were truncated.
func renderAgentLog(entries []contracts.LogEntry) (string, bool) {
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
