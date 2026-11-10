package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
)

// Server exposes the daemon over HTTP: a health probe, an authenticated
// JSON-RPC WebSocket, and the tool endpoint agents call from their shell.
type Server struct {
	disp   *Dispatcher
	engine *domain.Engine
	runner *orchestrator.Runner
	token  string
	box    *toolbox.Box

	connMu sync.Mutex
	conns  map[*conn]struct{}
}

// NewServer builds the HTTP surface. An empty token disables auth (dev only).
func NewServer(disp *Dispatcher, engine *domain.Engine, runner *orchestrator.Runner, token string, box *toolbox.Box) *Server {
	return &Server{disp: disp, engine: engine, runner: runner, token: token, box: box}
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/tool/call", s.handleToolCall)
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
	case toolbox.KindSubthread:
		s.handleSubthreadCall(w, r, req)
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
		Harness:   req.Harness,
		Model:     req.Model,
		DependsOn: req.DependsOn,
		Text:      req.Text,
	})
	s.disp.auditLog("tool", map[string]any{
		"kind": req.Kind, "agentId": string(req.AgentID), "threadId": string(req.ThreadID),
		"name": req.Name, "role": req.Role, "task": req.Task,
	})

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
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
	Harness   string             `json:"harness"`
	Model     string             `json:"model"`
	DependsOn []string           `json:"dependsOn"`
	Status    string             `json:"status"`
	Text      string             `json:"text"`
	To        string             `json:"to"`
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

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+s.token {
		return true
	}
	return r.URL.Query().Get("token") == s.token
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
