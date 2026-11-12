package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/files"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// envShellMaxTimeout bounds what a lead may ask a shell call to run for.
const envShellMaxTimeout = 10 * time.Minute

// handleEnvCall serves read-file, write-file and shell on behalf of a team lead.
// The lead works in the thread's own working tree: relative paths resolve there,
// and file paths and --cwd may not leave it, symlinks included. Only a lead on
// this machine may call these; a remote peer or a subagent's call is refused
// here, not queued, so the refusal reaches the caller.
func (s *Server) handleEnvCall(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	if !isLoopbackPeer(r) {
		http.Error(w, "tool calls are accepted only from this machine", http.StatusForbidden)
		return
	}
	if !orchestrator.IsLead(req.AgentID) {
		http.Error(w, "only the orchestrator can read, write, or run commands in the environment", http.StatusForbidden)
		return
	}
	workdir := s.disp.threadWorkdir(r.Context(), req.ThreadID)
	if workdir == "" {
		http.Error(w, "thread has no working directory", http.StatusBadRequest)
		return
	}
	switch toolbox.Kind(req.Kind) {
	case toolbox.KindReadFile:
		s.envReadFile(w, req, workdir)
	case toolbox.KindWriteFile:
		s.envWriteFile(w, req, workdir)
	case toolbox.KindShell:
		s.envShell(w, r, req, workdir)
	}
}

func (s *Server) envReadFile(w http.ResponseWriter, req toolCallRequest, workdir string) {
	path := strings.TrimSpace(req.Path)
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	res, err := files.Read(workdir, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.envAudit(req, map[string]any{"path": path, "size": res.Size})
	writeToolJSON(w, res)
}

func (s *Server) envWriteFile(w http.ResponseWriter, req toolCallRequest, workdir string) {
	path := strings.TrimSpace(req.Path)
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if len(req.Text) > files.MaxWriteBytes {
		http.Error(w, "file content is over the write limit", http.StatusBadRequest)
		return
	}
	if err := files.Write(workdir, path, []byte(req.Text)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.envAudit(req, map[string]any{"path": path, "bytes": len(req.Text)})
	writeToolJSON(w, map[string]any{"ok": true, "path": path, "bytes": len(req.Text)})
}

// envShell runs a command in the thread's worktree, or in --cwd under it. --cwd
// is relative and may not contain "..", and it is resolved like a file path, so
// a link that points outside the worktree is refused too. The command runs under the running turn's context, so Stop cancels it, and under
// the caller's request, so a lead that goes away takes its command with it. The
// command's output is also recorded in the thread log, as a user shell is.
func (s *Server) envShell(w http.ResponseWriter, r *http.Request, req toolCallRequest, workdir string) {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}
	dir, err := shellDir(workdir, strings.TrimSpace(req.Cwd))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		http.Error(w, "working directory is not a directory: "+req.Cwd, http.StatusBadRequest)
		return
	}

	ctx, done := s.disp.toolContext(r.Context(), req.ThreadID)
	defer done()
	res, err := runShell(ctx, shellRun{
		Dir: dir, Command: command, MaxBytes: shellOutputCap,
		Timeout: envShellTimeout(req.TimeoutSec), Started: s.disp.shellStarted,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The transcript is written even when the command was stopped, so the
	// partial output is not lost; the request context may already be done.
	if err := s.disp.recordShell(context.WithoutCancel(r.Context()), req.ThreadID, command, res); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.envAudit(req, map[string]any{"exitCode": res.ExitCode, "timedOut": res.TimedOut, "stopped": res.Stopped})
	writeToolJSON(w, map[string]any{
		"ok": true, "exitCode": res.ExitCode, "output": res.Output, "truncated": res.Truncated,
		"timedOut": res.TimedOut, "stopped": res.Stopped,
	})
}

// shellDir resolves a shell call's --cwd under the worktree. Empty or "." is the
// worktree itself. Absolute paths and any ".." are refused outright, so the
// request says what it means and cannot silently clamp to the root.
func shellDir(workdir, cwd string) (string, error) {
	if cwd == "" || cwd == "." {
		return workdir, nil
	}
	if filepath.IsAbs(cwd) {
		return "", fmt.Errorf("--cwd must be relative to the worktree: %q", cwd)
	}
	for _, part := range strings.Split(filepath.ToSlash(cwd), "/") {
		if part == ".." {
			return "", fmt.Errorf("--cwd may not contain \"..\": %q", cwd)
		}
	}
	dir, err := files.Resolve(workdir, cwd)
	if err != nil {
		return "", fmt.Errorf("--cwd: %w", err)
	}
	return dir, nil
}

// envShellTimeout is the lead's requested run time: the shell default when
// unset, capped at envShellMaxTimeout.
func envShellTimeout(sec int) time.Duration {
	switch {
	case sec <= 0:
		return shellTimeout
	case sec >= int(envShellMaxTimeout/time.Second):
		return envShellMaxTimeout
	default:
		return time.Duration(sec) * time.Second
	}
}

func (s *Server) envAudit(req toolCallRequest, fields map[string]any) {
	fields["kind"] = req.Kind
	fields["agentId"] = string(req.AgentID)
	fields["threadId"] = string(req.ThreadID)
	s.disp.auditLog("tool", fields)
}

func writeToolJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
