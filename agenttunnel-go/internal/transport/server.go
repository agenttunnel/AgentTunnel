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

	var req struct {
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
		Text      string             `json:"text"`
		To        string             `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ThreadID == "" || req.Kind == "" {
		http.Error(w, "threadId and kind are required", http.StatusBadRequest)
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

	c := &conn{ws: ws, engine: s.engine, disp: s.disp}
	c.serve(r.Context())
}

type conn struct {
	ws     *websocket.Conn
	engine *domain.Engine
	disp   *Dispatcher

	writeMu sync.Mutex
	unsubs  []func()
}

func (c *conn) send(resp Response) {
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
	defer func() {
		for _, u := range c.unsubs {
			u()
		}
	}()

	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			continue
		}
		if req.Method == "thread.subscribe" {
			c.subscribe(ctx, req)
			continue
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
}

func (c *conn) subscribe(ctx context.Context, req Request) {
	var p threadParams
	_ = json.Unmarshal(req.Params, &p)
	events, unsub := c.engine.Subscribe(p.ThreadID)
	c.unsubs = append(c.unsubs, unsub)
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
