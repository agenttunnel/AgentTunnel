package transport

import (
	"net/http"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
)

// handleAgentLogsCall serves agent-logs on behalf of a team lead: the full
// output log of one dispatched subagent, oldest first. A lead asks for it when
// a result summary is missing detail. Only a lead on this machine may call it;
// a remote peer's or a subagent's call is refused here, not queued, so the
// refusal reaches the caller.
//
// Name resolution and log rendering are shared with the revive tool
// (orchestrator.ResolveLogTarget, orchestrator.RenderAgentLog), so the same
// --to value names the same agent and its log renders identically through
// both surfaces.
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
	id, ok := orchestrator.ResolveLogTarget(st, to)
	if !ok {
		http.Error(w, "unknown agent "+to, http.StatusBadRequest)
		return
	}
	name := st.Agents[id].Name
	entries := s.runner.Logs().AgentEntries(req.ThreadID, id)
	text, truncated := orchestrator.RenderAgentLog(entries)
	s.envAudit(req, map[string]any{"target": string(id)})
	writeToolJSON(w, map[string]any{
		"ok": true, "agent": string(id), "name": name,
		"lines": len(entries), "truncated": truncated, "text": text,
	})
}
