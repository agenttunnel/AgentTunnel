package transport

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/attachments"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// readLimit caps one inbound client frame on the daemon side. A
// thread.turn.start with a long prompt or attachments can exceed websocket's
// 32768-byte default, which closed the connection before the RPC ran. It
// mirrors the relay's MaxFrameBytes convention (pkg/relay/protocol.go).
const readLimit = 4 << 20

// Server exposes the daemon over HTTP: a health probe, an authenticated
// JSON-RPC WebSocket, the tool endpoint agents call from their shell, and the
// attachment upload/download surface image data rides instead of the socket.
type Server struct {
	disp        *Dispatcher
	engine      *domain.Engine
	runner      *orchestrator.Runner
	token       string
	box         *toolbox.Box
	attachments *attachments.Store

	connMu sync.Mutex
	conns  map[*conn]struct{}
}

// NewServer builds the HTTP surface. An empty token disables auth (dev only).
// A nil attachment store keeps /attachments answering, but refuseful.
func NewServer(disp *Dispatcher, engine *domain.Engine, runner *orchestrator.Runner, token string, box *toolbox.Box, attach *attachments.Store) *Server {
	return &Server{disp: disp, engine: engine, runner: runner, token: token, box: box, attachments: attach}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/tool/call", s.handleToolCall)
	mux.HandleFunc("/attachments", s.handleAttachments)
	mux.HandleFunc("/ws", s.handleWS)
	return mux
}

// handleToolCall records a call made by the `atd tool` command on behalf of a
// running agent.
func (s *Server) handleToolCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.box == nil {
		http.Error(w, "tool inbox unavailable", http.StatusServiceUnavailable)
		return
	}

	var req toolCallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ThreadID == "" || req.Kind == "" {
		http.Error(w, "threadId and kind are required", http.StatusBadRequest)
		return
	}

	// Task calls apply at once rather than queueing: a lead's task-list must
	// reflect its own create and update earlier in the same pass.
	switch toolbox.Kind(req.Kind) {
	case toolbox.KindTaskCreate, toolbox.KindTaskUpdate, toolbox.KindTaskList:
		s.handleTaskCall(w, r, req)
		return
	case toolbox.KindThreadList, toolbox.KindThreadDelete:
		s.handleThreadCall(w, r, req)
		return
	case toolbox.KindReadFile, toolbox.KindWriteFile, toolbox.KindShell:
		s.handleEnvCall(w, r, req)
		return
	case toolbox.KindSubthread:
		s.handleSubthreadCall(w, r, req)
		return
	case toolbox.KindAsk:
		s.handleAskCall(w, r, req)
		return
	case toolbox.KindReviewComment, toolbox.KindReviewReply, toolbox.KindReviewResolve:
		s.handleReviewCall(w, r, req)
		return
	case toolbox.KindSteer:
		s.handleSteerCall(w, r, req)
		return
	case toolbox.KindAgentLogs:
		s.handleAgentLogsCall(w, r, req)
		return
	}

	// Peer messages are delivered immediately rather than queued in the tool
	// inbox: their recipients are running now, and the orchestrator would only
	// relay them anyway.
	if req.Kind == "message" {
		if s.runner == nil {
			http.Error(w, "peer messaging is unavailable", http.StatusServiceUnavailable)
			return
		}
		if strings.TrimSpace(req.To) == "" || strings.TrimSpace(req.Text) == "" {
			http.Error(w, "to and text are required for a message", http.StatusBadRequest)
			return
		}
		if err := s.runner.PostMessage(r.Context(), req.ThreadID, req.AgentID, req.To, req.Text); err != nil {
			if errors.Is(err, orchestrator.ErrUnknownRecipient) && s.box.HasDispatchNamed(req.ThreadID, req.To) {
				// The sender — typically the lead mid-pass — named an agent whose
				// dispatch is still sitting in the inbox, not yet recorded in the
				// thread state. Defer the message beside it; the orchestrator
				// delivers both once the pass ends and the dispatch is minted.
				s.box.Post(req.ThreadID, toolbox.Call{
					Kind:    toolbox.KindMessage,
					AgentID: req.AgentID,
					To:      req.To,
					Text:    req.Text,
				})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			if errors.Is(err, orchestrator.ErrPeerMailboxDisabled) || errors.Is(err, orchestrator.ErrUnknownRecipient) || errors.Is(err, orchestrator.ErrEmptyMessage) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.disp.auditLog("tool", map[string]any{
			"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
			"to": req.To,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}

	s.box.Post(req.ThreadID, toolbox.Call{
		Kind:      toolbox.Kind(req.Kind),
		AgentID:   req.AgentID,
		Name:      req.Name,
		Lane:      req.Lane,
		Role:      contracts.RoleID(req.Role),
		Task:      req.Task,
		Brief:     req.Brief,
		Harness:   req.Harness,
		Model:     req.Model,
		DependsOn: req.DependsOn,
		Text:      req.Text,
		// The queued call keeps its target thread: thread-relaunch and
		// thread-results ride it, and a call that lost it is refused by the
		// drain ("target thread is required"). (The kinds that name a target
		// through Target — thread-delete — are routed earlier in the switch.)
		Thread: req.Thread,
		To:     req.To,
	})
	s.disp.auditLog("tool", map[string]any{
		"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
		"name": req.Name, "role": req.Role, "task": req.Task,
	})

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleAttachments is the image surface: POST with raw image bytes stores
// one under the thread and answers with its meta; GET fetches the bytes back
// by id. Image data never rides the WebSocket.
func (s *Server) handleAttachments(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.attachments == nil {
		http.Error(w, "image attachments are unavailable", http.StatusServiceUnavailable)
		return
	}
	threadID := contracts.ThreadID(r.URL.Query().Get("threadId"))
	if threadID == "" {
		http.Error(w, "threadId is required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		st, err := s.engine.State(r.Context(), threadID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !st.Exists {
			http.Error(w, "thread does not exist", http.StatusNotFound)
			return
		}
		// One byte of headroom so an over-cap upload reads as too large
		// rather than a connection error mid-body.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, contracts.MaxImageBytes+1))
		if err != nil {
			http.Error(w, "image exceeds the size limit", http.StatusRequestEntityTooLarge)
			return
		}
		ref, err := s.attachments.Save(threadID, r.URL.Query().Get("name"), body)
		if err != nil {
			http.Error(w, err.Error(), badAttachmentStatus(err))
			return
		}
		s.disp.auditLog("attachment.save", map[string]any{
			"threadId": string(threadID), "id": ref.ID, "mime": ref.Mime, "size": ref.Size,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ref)

	case http.MethodGet:
		id := r.URL.Query().Get("id")
		if !contracts.ValidImageID(id) {
			http.Error(w, "invalid attachment id", http.StatusBadRequest)
			return
		}
		ref, data, err := s.attachments.Open(threadID, id)
		if err != nil {
			http.Error(w, err.Error(), badAttachmentStatus(err))
			return
		}
		w.Header().Set("Content-Type", ref.Mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// badAttachmentStatus maps store errors to the client-facing status code:
// anything about the bytes' shape or the pair's identity is a 400, a missing
// pair is a 404, and the rest are the server's fault.
func badAttachmentStatus(err error) int {
	switch {
	case errors.Is(err, attachments.ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, attachments.ErrUnsupportedMime), errors.Is(err, attachments.ErrInvalidID):
		return http.StatusBadRequest
	case errors.Is(err, attachments.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

// toolCallRequest is the body the `atd tool` command posts to /tool/call.
type toolCallRequest struct {
	ThreadID  contracts.ThreadID `json:"threadId"`
	AgentID   contracts.AgentID  `json:"agentId"`
	Kind      string             `json:"kind"`
	Name      string             `json:"name"`
	Lane      string             `json:"lane"`
	Role      string             `json:"role"`
	Task      string             `json:"task"`
	Brief     string             `json:"brief"`
	Harness   string             `json:"harness"`
	Model     string             `json:"model"`
	DependsOn []string           `json:"dependsOn"`
	Status    string             `json:"status"`
	// Text is the question for an ask; To names the agent that also hears the
	// answer.
	Text string `json:"text"`
	To   string `json:"to"`
	// Target names the thread a thread-delete removes; RemoveWorktree also
	// removes that thread's own worktree.
	Target         string `json:"target"`
	RemoveWorktree bool   `json:"removeWorktree"`
	// Path names the file a read-file or write-file touches, relative to the
	// thread's worktree. Command and Cwd shape a shell call; TimeoutSec, as for
	// an ask, bounds it.
	Path    string `json:"path"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	// QuestionKind, Options and TimeoutSec shape an ask.
	QuestionKind string   `json:"questionKind"`
	Options      []string `json:"options"`
	TimeoutSec   int      `json:"timeoutSec"`
	// File, Line, EndLine and Side anchor a review comment; Thread names the
	// review thread a reply or resolve answers; Reopen moves it back to open.
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"endLine"`
	Side    string `json:"side"`
	Thread  string `json:"thread"`
	Reopen  bool   `json:"reopen"`
}

// handleReviewCall applies a review-comment, review-reply or review-resolve call
// at once and answers with the review thread's id. The decider decides who may
// do what, so a refusal (an executor resolving, an unknown thread) is a 400 the
// agent sees in the same pass.
func (s *Server) handleReviewCall(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	if s.runner == nil {
		http.Error(w, "review threads are unavailable", http.StatusServiceUnavailable)
		return
	}
	id, err := s.runner.ReviewCall(r.Context(), req.ThreadID, req.AgentID, toolbox.Call{
		Kind:    toolbox.Kind(req.Kind),
		AgentID: req.AgentID,
		Text:    req.Text,
		File:    req.File,
		Line:    req.Line,
		EndLine: req.EndLine,
		Side:    req.Side,
		Thread:  req.Thread,
		Reopen:  req.Reopen,
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrInvalidCommand) || errors.Is(err, domain.ErrThreadNotFound) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.disp.auditLog("tool", map[string]any{
		"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
		"reviewThread": id,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id})
}

// handleTaskCall applies a task-create, task-update or task-list call to the
// thread's agreed task list. Creates and updates answer ok; a list answers with
// the tasks themselves.
func (s *Server) handleTaskCall(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	ctx := r.Context()
	var resp any = map[string]any{"ok": true}
	var err error
	switch toolbox.Kind(req.Kind) {
	case toolbox.KindTaskCreate:
		err = s.engine.Dispatch(ctx, contracts.Command{
			ID: s.disp.nextID(), Type: contracts.CmdTaskAdd, ThreadID: req.ThreadID,
			TaskAdd: &contracts.TaskAdded{Name: req.Name, Title: req.Task},
		})
	case toolbox.KindTaskUpdate:
		err = s.engine.Dispatch(ctx, contracts.Command{
			ID: s.disp.nextID(), Type: contracts.CmdTaskStatusSet, ThreadID: req.ThreadID,
			TaskStatus: &contracts.TaskStatusChanged{Name: req.Name, Status: contracts.TaskStatus(req.Status)},
		})
	case toolbox.KindTaskList:
		var st *domain.ThreadState
		st, err = s.engine.State(ctx, req.ThreadID)
		if err == nil {
			tasks := st.Tasks
			if tasks == nil {
				tasks = []domain.TaskItem{}
			}
			resp = map[string]any{"ok": true, "tasks": tasks}
		}
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrInvalidCommand) || errors.Is(err, domain.ErrThreadNotFound) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.disp.auditLog("tool", map[string]any{
		"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
		"name": req.Name, "status": req.Status,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleSteerCall queues the lead's steer for one subagent and answers with the
// outcome, so the lead learns at once whether it was queued. It bypasses the
// tool inbox: the steer must not wait for the pass to end.
func (s *Server) handleSteerCall(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	if s.runner == nil {
		http.Error(w, "steering is unavailable", http.StatusServiceUnavailable)
		return
	}
	if strings.TrimSpace(req.To) == "" || strings.TrimSpace(req.Text) == "" {
		http.Error(w, "to and text are required for a steer", http.StatusBadRequest)
		return
	}
	reply, err := s.runner.Steer(r.Context(), req.ThreadID, req.AgentID, req.To, req.Text)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, orchestrator.ErrSteerNotLead):
			status = http.StatusForbidden
		case errors.Is(err, orchestrator.ErrUnknownRecipient),
			errors.Is(err, orchestrator.ErrSteerTargetLead),
			errors.Is(err, orchestrator.ErrSteerFinished),
			errors.Is(err, orchestrator.ErrEmptyMessage),
			errors.Is(err, domain.ErrThreadNotFound):
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.disp.auditLog("tool", map[string]any{
		"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
		"to": req.To,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "outcome": "queued", "message": reply})
}

// handleSubthreadCall starts a subthread for the caller's thread. Only a team
// lead may start one; a subagent's request is refused here, not queued, so the
// refusal reaches the agent that asked.
func (s *Server) handleSubthreadCall(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	if req.AgentID != "" && !orchestrator.IsLead(req.AgentID) {
		http.Error(w, "only the orchestrator can start a subthread", http.StatusForbidden)
		return
	}
	child, err := s.disp.startSubthread(r.Context(), req.ThreadID, req.Task)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrInvalidCommand) || errors.Is(err, domain.ErrThreadNotFound) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.disp.auditLog("tool", map[string]any{
		"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
		"subthreadId": string(child),
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "threadId": child})
}

// toolThread is one row of `tool thread-list`.
type toolThread struct {
	ID             contracts.ThreadID `json:"id"`
	Title          string             `json:"title"`
	Status         string             `json:"status"`
	ProjectID      string             `json:"projectId,omitempty"`
	ParentThreadID contracts.ThreadID `json:"parentThreadId,omitempty"`
	Branch         string             `json:"branch,omitempty"`
	Worktree       string             `json:"worktree,omitempty"`
}

// handleThreadCall lists the environment's threads, or deletes one, on behalf
// of a team lead. Only a lead may do either: a subagent's call is refused here,
// not queued, so the refusal reaches the agent that asked. A lead may not delete
// its own thread, and deleteThread refuses any thread with work still running.
// Like every tool call, it is accepted only from this machine: the agent id is
// a body field a remote caller can set to anything.
func (s *Server) handleThreadCall(w http.ResponseWriter, r *http.Request, req toolCallRequest) {
	if !isLoopbackPeer(r) {
		http.Error(w, "tool calls are accepted only from this machine", http.StatusForbidden)
		return
	}
	if !orchestrator.IsLead(req.AgentID) {
		http.Error(w, "only the orchestrator can list or delete threads", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	var resp any = map[string]any{"ok": true}
	var err error
	switch toolbox.Kind(req.Kind) {
	case toolbox.KindThreadList:
		var rows []toolThread
		rows, err = s.threadList(ctx)
		resp = map[string]any{"ok": true, "threads": rows}
	case toolbox.KindThreadDelete:
		target := contracts.ThreadID(strings.TrimSpace(req.Target))
		switch {
		case target == "":
			err = fmt.Errorf("%w: target thread id is required", domain.ErrInvalidCommand)
		case target == req.ThreadID:
			err = fmt.Errorf("%w: a thread cannot delete itself", domain.ErrInvalidCommand)
		default:
			err = s.disp.deleteThread(ctx, target, req.RemoveWorktree)
			s.disp.auditLog("tool", map[string]any{
				"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
				"target": string(target), "removeWorktree": req.RemoveWorktree,
			})
		}
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrInvalidCommand) || errors.Is(err, domain.ErrThreadNotFound) {
			status = http.StatusBadRequest
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// threadList reuses the thread.list RPC, so the rows match what the desktop
// sees, and adds the lead-facing status and branch.
func (s *Server) threadList(ctx context.Context) ([]toolThread, error) {
	out, err := s.disp.handle(ctx, Request{Method: "thread.list"})
	if err != nil {
		return nil, err
	}
	summaries, ok := out.([]ThreadSummary)
	if !ok {
		return nil, errors.New("thread list unavailable")
	}
	rows := make([]toolThread, 0, len(summaries))
	for _, t := range summaries {
		row := toolThread{ID: t.ID, Title: t.Title, Status: "idle", ProjectID: t.ProjectID, ParentThreadID: t.ParentThreadID}
		if t.RunningAgents > 0 || s.disp.threadRunning(t.ID) {
			row.Status = "running"
		}
		if t.Worktree != nil {
			row.Branch = t.Worktree.Branch
			row.Worktree = t.Worktree.Path
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// authorized accepts the token as "Authorization: Bearer" from any peer. The
// ?token= query form is accepted only from a loopback peer: query strings end
// up in proxy and access logs, so a remote client must use the header.
func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && tokenMatches(bearer, s.token) {
		return true
	}
	return isLoopbackPeer(r) && tokenMatches(r.URL.Query().Get("token"), s.token)
}

// tokenMatches compares in constant time so a mismatch cannot reveal how much
// of the token was right.
func tokenMatches(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// isLoopbackPeer reports whether the request came from this machine. An
// unparseable peer address counts as remote.
func isLoopbackPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Native clients and the desktop webview are same-origin-less; enforce
		// auth via the token instead of the Origin header.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(readLimit)
	defer ws.CloseNow()

	c := &conn{ws: ws, engine: s.engine, disp: s.disp, srv: s}
	s.connMu.Lock()
	if s.conns == nil {
		s.conns = map[*conn]struct{}{}
	}
	s.conns[c] = struct{}{}
	s.connMu.Unlock()
	defer func() {
		s.connMu.Lock()
		delete(s.conns, c)
		s.connMu.Unlock()
	}()
	c.serve(r.Context())
}

// NotifyAll pushes a frame to every connected client, for cross-client
// notifications like the desktop's open-thread handoff. Slow or closing
// connections simply miss the frame: these are hints, not guarantees.
func (s *Server) NotifyAll(method string, params any) {
	s.connMu.Lock()
	snapshot := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		snapshot = append(snapshot, c)
	}
	s.connMu.Unlock()
	for _, c := range snapshot {
		c.send(Response{Method: method, Params: params})
	}
}

// handleAppOpen broadcasts an `app.open` frame to every client — the running
// desktop opens the named thread and focuses its window. Without a threadId it
// is a focus-only nudge. Handled here rather than in the dispatcher so the
// server owns its connection registry.
func (s *Server) handleAppOpen(c *conn, req Request) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(req.Params, &p)
	params := map[string]any{}
	if p.ThreadID != "" {
		st, err := s.engine.State(context.Background(), contracts.ThreadID(p.ThreadID))
		if err != nil {
			c.send(Response{ID: req.ID, Error: &RPCError{Code: 1, Message: err.Error()}})
			return
		}
		params["threadId"] = p.ThreadID
		params["projectId"] = st.ProjectID
	}
	s.NotifyAll("app.open", params)
	c.send(Response{ID: req.ID, Result: map[string]any{"ok": true}})
}

// conn is one JSON-RPC client: a /ws socket, or a Session whose frames arrive
// through a relay. Both share the same dispatch and subscription behavior.
type conn struct {
	ws     *websocket.Conn // nil for a Session
	sink   func(Response)  // set for a Session
	engine *domain.Engine
	disp   *Dispatcher
	srv    *Server

	writeMu sync.Mutex
	subMu   sync.Mutex
	unsubs  []func()
}

func (c *conn) send(resp Response) {
	if c.sink != nil {
		c.sink(resp)
		return
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.Write(ctx, websocket.MessageText, data)
}

func (c *conn) serve(ctx context.Context) {
	defer c.unsubscribeAll()

	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		c.handleFrame(ctx, data)
	}
}

// handleFrame dispatches one client->server JSON-RPC frame. Requests run on
// their own goroutine so a slow turn never blocks the reader.
func (c *conn) handleFrame(ctx context.Context, data []byte) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return
	}
	if req.Method == "thread.subscribe" {
		c.subscribe(ctx, req)
		return
	}
	if req.Method == "app.open" {
		c.srv.handleAppOpen(c, req)
		return
	}
	if req.Method == "daemon.shutdown" {
		// Acknowledge before tearing the server down: the client's `agenttunnel daemon
		// stop` waits for this reply, then for daemon.json to disappear.
		c.send(Response{ID: req.ID, Result: map[string]any{"ok": true}})
		go c.disp.Shutdown()
		return
	}
	go func() {
		result, err := c.disp.Handle(ctx, req)
		if err != nil {
			c.send(Response{ID: req.ID, Error: &RPCError{Code: 1, Message: err.Error()}})
			return
		}
		c.send(Response{ID: req.ID, Result: result})
	}()
}

func (c *conn) unsubscribeAll() {
	c.subMu.Lock()
	unsubs := c.unsubs
	c.unsubs = nil
	c.subMu.Unlock()
	for _, u := range unsubs {
		u()
	}
}

func (c *conn) subscribe(ctx context.Context, req Request) {
	var p threadParams
	_ = json.Unmarshal(req.Params, &p)
	events, unsub := c.engine.Subscribe(p.ThreadID)
	c.subMu.Lock()
	c.unsubs = append(c.unsubs, unsub)
	c.subMu.Unlock()
	c.send(Response{ID: req.ID, Result: map[string]any{"subscribed": p.ThreadID}})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				c.send(Response{Method: "thread.event", Params: ev})
			}
		}
	}()
}
