package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/actionlog"
	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/files"
	"github.com/agenttunnel/agenttunnel-go/internal/gitdiff"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/scm"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// Request is a client->server RPC call.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is a server->client reply or, when Method is set, a pushed event.
type Response struct {
	ID     string    `json:"id,omitempty"`
	Result any       `json:"result,omitempty"`
	Error  *RPCError `json:"error,omitempty"`
	Method string    `json:"method,omitempty"`
	Params any       `json:"params,omitempty"`
}

// RPCError is a JSON-RPC style error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// DispatcherDeps are the services an RPC dispatcher routes to.
type DispatcherDeps struct {
	Engine       *domain.Engine
	Runner       *orchestrator.Runner
	Orchestrator *orchestrator.Orchestrator
	Ledger       *usage.Ledger
	Logs         *logstore.Store
	Roles        *contracts.RoleRegistry
	Registry     *providers.Registry
	Settings     *settings.Store
	Models       *models.Service
	Steering     *steering.Box
	Audit        actionlog.Func
}

// Dispatcher routes RPC methods to the daemon's services.
type Dispatcher struct {
	engine   *domain.Engine
	runner   *orchestrator.Runner
	orch     *orchestrator.Orchestrator
	ledger   *usage.Ledger
	logs     *logstore.Store
	roles    *contracts.RoleRegistry
	registry *providers.Registry
	settings *settings.Store
	models   *models.Service
	steering *steering.Box
	audit    actionlog.Func
	seq      atomic.Int64

	// turns gates one running turn per thread and lets clients stop it.
	turnsMu sync.Mutex
	turns   map[contracts.ThreadID]context.CancelFunc

	// shellStarted, when set by a test, runs once a thread.runCommand child is
	// live. Nil in production.
	shellStarted func()
}

// NewDispatcher constructs a dispatcher.
func NewDispatcher(d DispatcherDeps) *Dispatcher {
	return &Dispatcher{
		engine:   d.Engine,
		runner:   d.Runner,
		orch:     d.Orchestrator,
		ledger:   d.Ledger,
		logs:     d.Logs,
		roles:    d.Roles,
		registry: d.Registry,
		settings: d.Settings,
		models:   d.Models,
		steering: d.Steering,
		audit:    d.Audit,
	}
}

// ThreadSummary is the list-row projection of a thread.
type ThreadSummary struct {
	ID        contracts.ThreadID `json:"id"`
	Title     string             `json:"title"`
	ProjectID string             `json:"projectId,omitempty"`
	UpdatedAt time.Time          `json:"updatedAt"`
	Messages  int                `json:"messages"`
	Agents    int                `json:"agents"`
	AutoMode  bool               `json:"autoMode"`

	// Staleness is derived: a thread with running agents never goes stale.
	RunningAgents     int  `json:"runningAgents"`
	StaleAfterSeconds int  `json:"staleAfterSeconds"`
	Stale             bool `json:"stale"`
}

// AgentView pairs an agent with its accumulated usage.
type AgentView struct {
	Agent contracts.Agent      `json:"agent"`
	Usage contracts.TokenUsage `json:"usage"`
}

type (
	createThreadParams struct {
		Title     string `json:"title"`
		ProjectID string `json:"projectId"`
	}
	threadParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	turnParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Text     string             `json:"text"`
	}
	setBoolParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Enabled  bool               `json:"enabled"`
	}
	agentParams struct {
		AgentID contracts.AgentID `json:"agentId"`
	}
	agentLogParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		AgentID  contracts.AgentID  `json:"agentId"`
	}
)

// threadLogTail bounds thread.logs: the desktop log box shows this many recent
// lines, so the restore never ships a whole long-running thread over the socket.
const threadLogTail = 400

const defaultStaleAfterSeconds = 30 * 60

// beginTurn claims a thread's turn slot, returning a context that ends when the
// turn is stopped. The second call for the same thread fails; turns are serial
// per thread.
func (d *Dispatcher) beginTurn(ctx context.Context, threadID contracts.ThreadID) (context.Context, func(), error) {
	d.turnsMu.Lock()
	defer d.turnsMu.Unlock()
	if d.turns == nil {
		d.turns = map[contracts.ThreadID]context.CancelFunc{}
	}
	if _, running := d.turns[threadID]; running {
		return nil, nil, fmt.Errorf("a turn is already running on thread %s", threadID)
	}
	turnCtx, cancel := context.WithCancel(ctx)
	d.turns[threadID] = cancel
	return turnCtx, func() {
		cancel()
		d.turnsMu.Lock()
		defer d.turnsMu.Unlock()
		// The slot is released only here, never in stopTurn: a stopped turn
		// holds the slot until it actually exits, so a new turn cannot begin
		// while the old one is winding down.
		delete(d.turns, threadID)
	}, nil
}

// stopTurn cancels a running turn and reports whether one was found. The
// thread's turn slot stays taken until the turn finishes unwinding.
func (d *Dispatcher) stopTurn(threadID contracts.ThreadID) bool {
	d.turnsMu.Lock()
	cancel, running := d.turns[threadID]
	d.turnsMu.Unlock()
	if running {
		cancel()
	}
	return running
}

// Handle executes one RPC method, recording it in the action log.
func (d *Dispatcher) Handle(ctx context.Context, req Request) (any, error) {
	start := time.Now()
	result, err := d.handle(ctx, req)
	d.auditLog("rpc", map[string]any{
		"method": req.Method,
		"ms":     time.Since(start).Milliseconds(),
		"error":  errText(err),
	})
	return result, err
}

func (d *Dispatcher) auditLog(kind string, fields map[string]any) {
	if d.audit != nil {
		d.audit(kind, fields)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (d *Dispatcher) handle(ctx context.Context, req Request) (any, error) {
	switch req.Method {
	case "roles.list":
		return d.roles.List(), nil

	case "harness.list":
		enabled := make([]contracts.Harness, 0, len(contracts.AllHarnesses()))
		modelsByHarness := make(map[string][]string, len(contracts.AllHarnesses()))
		for _, h := range contracts.AllHarnesses() {
			if d.settings == nil || d.settings.HarnessEnabled(h) {
				enabled = append(enabled, h)
			}
			if d.models != nil {
				modelsByHarness[string(h)] = d.models.List(h)
			}
		}
		return map[string]any{
			"all":       contracts.AllHarnesses(),
			"available": d.registry.Available(),
			"enabled":   enabled,
			"models":    modelsByHarness,
		}, nil

	case "models.refresh":
		var p struct {
			Harness string `json:"harness"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		h := contracts.Harness(p.Harness)
		if !h.Valid() {
			return nil, fmt.Errorf("unknown harness %q", p.Harness)
		}
		if d.models == nil {
			return nil, errors.New("model discovery is unavailable")
		}
		return map[string]any{
			"harness": string(h),
			"models":  d.models.Refresh(ctx, h),
			"error":   d.models.LastError(h),
		}, nil

	case "models.add":
		var p struct {
			Harness string `json:"harness"`
			Model   string `json:"model"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		h := contracts.Harness(p.Harness)
		if !h.Valid() {
			return nil, fmt.Errorf("unknown harness %q", p.Harness)
		}
		if strings.TrimSpace(p.Model) == "" {
			return nil, errors.New("model is required")
		}
		if d.models == nil {
			return nil, errors.New("model discovery is unavailable")
		}
		d.models.Add(h, p.Model)
		return map[string]any{"harness": string(h), "models": d.models.List(h)}, nil

	case "thread.open":
		// The desktop CLI's `agenttunnel <dir>` flow: resolve the directory to
		// a project (creating one when the dir is unknown) and open a fresh
		// thread in it. Creation is provisional — closing the window without
		// typing leaves nothing behind.
		var p struct {
			Dir string `json:"dir"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Dir) == "" {
			return nil, errors.New("dir is required")
		}
		if d.settings == nil {
			return nil, errors.New("settings are unavailable")
		}
		resolved := resolveDirPath(p.Dir)
		var project settings.Project
		created := false
		for _, cand := range d.settings.Projects() {
			if resolveDirPath(cand.Path) == resolved {
				project = cand
				break
			}
		}
		if project.ID == "" {
			name := filepath.Base(resolved)
			if name == "" || name == "." || name == string(filepath.Separator) {
				name = "project"
			}
			project = settings.Project{ID: d.newProjectID(), Name: name, Path: resolved}
			d.settings.AddProject(project)
			created = true
		}
		id := d.newThreadID()
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdThreadCreate, ThreadID: id, ProjectID: project.ID,
		}); err != nil {
			return nil, err
		}
		return map[string]any{
			"threadId":       id,
			"projectId":      project.ID,
			"projectCreated": created,
			"projects":       d.projectList(ctx),
		}, nil

	case "thread.create":
		var p createThreadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		id := d.newThreadID()
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdThreadCreate, ThreadID: id,
			Title: p.Title, ProjectID: p.ProjectID,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"threadId": id}, nil

	case "thread.list":
		ids, err := d.engine.Threads(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]ThreadSummary, 0, len(ids))
		staleAfter := defaultStaleAfterSeconds
		if d.settings != nil {
			staleAfter = d.settings.StaleAfterMinutes() * 60
		}
		now := time.Now()
		if d.engine != nil {
			now = d.engine.Now()
		}
		for _, id := range ids {
			st, err := d.engine.State(ctx, id)
			if err != nil {
				return nil, err
			}
			running := 0
			for _, a := range st.Agents {
				if a.Status == contracts.AgentRunning || a.Status == contracts.AgentPending {
					running++
				}
			}
			stale := running == 0 && staleAfter > 0 && now.Sub(st.UpdatedAt) >= time.Duration(staleAfter)*time.Second
			out = append(out, ThreadSummary{
				ID: id, Title: st.Title, ProjectID: st.ProjectID, UpdatedAt: st.UpdatedAt,
				Messages: len(st.Messages), Agents: len(st.Agents), AutoMode: st.AutoMode,
				RunningAgents: running, StaleAfterSeconds: staleAfter, Stale: stale,
			})
		}
		// The thread list is a recency list: newest activity first. Event stores
		// may enumerate files in any order, so sort explicitly.
		sort.Slice(out, func(i, j int) bool {
			if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
				return out[i].ID < out[j].ID
			}
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		})
		return out, nil

	case "thread.get":
		var p threadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.engine.State(ctx, p.ThreadID)

	case "thread.events":
		var p threadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		events, err := d.engine.Events(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		return graphEvents(events), nil

	case "thread.turn.start":
		var p turnParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Text) == "" {
			return nil, errors.New("turn text is required")
		}
		st, err := d.engine.State(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		if !st.Exists {
			return nil, fmt.Errorf("thread %q does not exist", p.ThreadID)
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdTurnStart, ThreadID: p.ThreadID, Text: p.Text,
		}); err != nil {
			return nil, err
		}
		turnCtx, done, err := d.beginTurn(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		defer done()
		if err := d.orch.RunTurn(turnCtx, p.ThreadID, p.Text); err != nil {
			if turnCtx.Err() != nil {
				// Stopped from the client: not an error, but say what happened.
				return map[string]any{"ok": true, "stopped": true}, nil
			}
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "thread.runCommand":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Command  string             `json:"command"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		command := strings.TrimSpace(p.Command)
		if command == "" {
			return nil, errors.New("command is required")
		}
		st, err := d.engine.State(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		if !st.Exists {
			return nil, fmt.Errorf("thread %q does not exist", p.ThreadID)
		}
		// The command takes the turn slot: it cannot interleave with agent work,
		// and the Stop button cancels it through the same context.
		turnCtx, done, err := d.beginTurn(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		defer done()
		res, err := runShell(turnCtx, shellRun{
			Dir: d.threadWorkdir(ctx, p.ThreadID), Command: command,
			MaxBytes: shellOutputCap, Timeout: shellTimeout,
			Started: d.shellStarted,
		})
		if err != nil {
			return nil, err
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID:       d.nextID(),
			Type:     contracts.CmdMessageAppend,
			ThreadID: p.ThreadID,
			Message:  &contracts.MessageSent{MessageID: d.nextID(), Author: "shell", Text: shellMessage(command, res)},
		}); err != nil {
			return nil, err
		}
		return map[string]any{
			"ok": true, "exitCode": res.ExitCode, "truncated": res.Truncated,
			"timedOut": res.TimedOut, "stopped": res.Stopped,
		}, nil

	case "thread.council.start":
		var p struct {
			ThreadID contracts.ThreadID           `json:"threadId"`
			Topic    string                       `json:"topic"`
			Rounds   int                          `json:"rounds"`
			Members  []orchestrator.CouncilMember `json:"members"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Topic) == "" {
			return nil, errors.New("council topic is required")
		}
		st, err := d.engine.State(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		if !st.Exists {
			return nil, fmt.Errorf("thread %q does not exist", p.ThreadID)
		}
		if d.orch == nil {
			return nil, errors.New("orchestrator is unavailable")
		}
		// The debate is a turn: the topic lands in the transcript and the turn
		// gate applies, so a council cannot race a normal turn.
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdTurnStart, ThreadID: p.ThreadID, Text: p.Topic,
		}); err != nil {
			return nil, err
		}
		turnCtx, done, err := d.beginTurn(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		defer done()
		spec := orchestrator.CouncilSpec{Topic: p.Topic, Rounds: p.Rounds, Members: p.Members}
		consensus, err := d.orch.RunCouncil(turnCtx, p.ThreadID, spec)
		if err != nil {
			if turnCtx.Err() != nil {
				return map[string]any{"ok": true, "stopped": true}, nil
			}
			return nil, err
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID:       d.nextID(),
			Type:     contracts.CmdMessageAppend,
			ThreadID: p.ThreadID,
			Message:  &contracts.MessageSent{MessageID: d.nextID(), Author: "council", Text: consensus},
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "consensus": consensus}, nil

	case "thread.turn.stop":
		var p threadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if p.ThreadID == "" {
			return nil, errors.New("threadId is required")
		}
		return map[string]any{"ok": true, "stopped": d.stopTurn(p.ThreadID)}, nil

	case "agents.stop":
		var p agentParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if p.AgentID == "" {
			return nil, errors.New("agentId is required")
		}
		if d.runner == nil {
			return nil, errors.New("runner is unavailable")
		}
		return map[string]any{"ok": true, "stopped": d.runner.StopAgent(p.AgentID)}, nil

	case "thread.rename":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Title    string             `json:"title"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Title) == "" {
			return nil, errors.New("title is required")
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID:       d.nextID(),
			Type:     contracts.CmdThreadRename,
			ThreadID: p.ThreadID,
			Title:    p.Title,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "thread.delete":
		var p threadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		st, err := d.engine.State(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		if !st.Exists {
			return nil, fmt.Errorf("thread %q does not exist", p.ThreadID)
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdThreadDelete, ThreadID: p.ThreadID,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "thread.steer":
		var p turnParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Text) == "" {
			return nil, errors.New("steering text is required")
		}
		st, err := d.engine.State(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		if !st.Exists {
			return nil, fmt.Errorf("thread %q does not exist", p.ThreadID)
		}
		if d.steering == nil {
			return nil, errors.New("steering is unavailable")
		}
		// Record it in the transcript, marked as steering, then queue it for the
		// next orchestrator pass to weigh.
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID:       d.nextID(),
			Type:     contracts.CmdMessageAppend,
			ThreadID: p.ThreadID,
			Message:  &contracts.MessageSent{MessageID: d.nextID(), Author: "steering", Text: p.Text},
		}); err != nil {
			return nil, err
		}
		d.steering.Post(p.ThreadID, p.Text)
		return map[string]any{"ok": true}, nil

	case "thread.setAutoMode":
		var p setBoolParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdAutoModeSet, ThreadID: p.ThreadID, AutoMode: &p.Enabled,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "thread.setPeerMailbox":
		var p setBoolParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdPeerMailboxSet, ThreadID: p.ThreadID, PeerMailbox: &p.Enabled,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "agents.list":
		var p threadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		st, err := d.engine.State(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		out := make([]AgentView, 0, len(st.AgentOrder))
		for _, id := range st.AgentOrder {
			out = append(out, AgentView{Agent: st.Agents[id], Usage: st.Usage[id].Usage})
		}
		return out, nil

	case "thread.logs":
		var p threadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		entries := d.logs.Entries(p.ThreadID)
		if len(entries) > threadLogTail {
			entries = entries[len(entries)-threadLogTail:]
		}
		return entries, nil

	case "agents.logs":
		var p agentLogParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.logs.AgentEntries(p.ThreadID, p.AgentID), nil

	case "usage.get":
		return d.ledger.Snapshot(), nil

	case "review.diff":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Workdir  string             `json:"workdir"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		wd := p.Workdir
		if wd == "" {
			wd = d.threadWorkdir(ctx, p.ThreadID)
		}
		return gitdiff.WorkingTreeDiff(ctx, wd)

	case "pr.status":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Workdir  string             `json:"workdir"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		wd := p.Workdir
		if wd == "" {
			wd = d.threadWorkdir(ctx, p.ThreadID)
		}
		return scm.Fetch(ctx, wd), nil

	case "files.tree":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Workdir  string             `json:"workdir"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		wd := p.Workdir
		if wd == "" {
			wd = d.threadWorkdir(ctx, p.ThreadID)
		}
		entries, err := files.Tree(ctx, wd)
		if err != nil {
			return nil, err
		}
		status, _ := gitdiff.Status(ctx, wd)
		byPath := make(map[string]gitdiff.StatusCode, len(status))
		for _, s := range status {
			byPath[s.Path] = s.Status
		}
		return map[string]any{"root": wd, "entries": entries, "status": byPath}, nil

	case "files.read":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Workdir  string             `json:"workdir"`
			Path     string             `json:"path"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		wd := p.Workdir
		if wd == "" {
			wd = d.threadWorkdir(ctx, p.ThreadID)
		}
		return files.Read(wd, p.Path)

	case "review.comment":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			File     string             `json:"file"`
			Line     int                `json:"line"`
			Body     string             `json:"body"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		comment := &contracts.ReviewCommentAdded{
			ID:        d.nextID(),
			File:      p.File,
			Line:      p.Line,
			Body:      p.Body,
			AuthorID:  "user",
			CreatedAt: time.Now().UTC(),
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID:            d.nextID(),
			Type:          contracts.CmdReviewCommentAdd,
			ThreadID:      p.ThreadID,
			ReviewComment: comment,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "settings.get":
		return d.settingsView(), nil

	case "settings.set":
		if d.settings == nil {
			return nil, errors.New("settings are not available")
		}
		var p settingsPatch
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if p.Workdir != nil {
			d.settings.SetWorkdir(*p.Workdir)
		}
		if p.AutoMode != nil {
			d.settings.SetAutoMode(*p.AutoMode)
		}
		if p.ApprovedModels != nil {
			d.settings.SetApprovedModels(*p.ApprovedModels)
		}
		if p.StaleAfterMinutes != nil {
			d.settings.SetStaleAfterMinutes(*p.StaleAfterMinutes)
		}
		if p.Role != nil {
			d.settings.SetRoleConfig(contracts.RoleID(p.Role.ID), settings.RoleConfig{
				Harness: contracts.Harness(p.Role.Harness),
				Model:   p.Role.Model,
				Variant: p.Role.Variant,
			})
		}
		if p.Harness != nil {
			d.settings.SetHarnessEnabled(contracts.Harness(p.Harness.ID), p.Harness.Enabled)
		}
		return d.settingsView(), nil

	case "context.inherited.get":
		inh := d.inherited()
		if inh == nil {
			return nil, errors.New("inherited context is unavailable")
		}
		guide, err := inh.Guide()
		if err != nil {
			return nil, err
		}
		logText, err := inh.Log()
		if err != nil {
			return nil, err
		}
		rules, err := inh.Rules()
		if err != nil {
			return nil, err
		}
		rendered, _ := inh.Render()
		return map[string]any{
			"dir": inh.Dir(), "guide": guide, "log": logText,
			"rules": rules, "rendered": rendered,
		}, nil

	case "context.inherited.save":
		var p struct {
			Guide *string `json:"guide"`
			Log   *string `json:"log"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		inh := d.inherited()
		if inh == nil {
			return nil, errors.New("inherited context is unavailable")
		}
		if p.Guide != nil {
			if err := inh.SetGuide(*p.Guide); err != nil {
				return nil, err
			}
		}
		if p.Log != nil {
			if err := inh.SetLog(*p.Log); err != nil {
				return nil, err
			}
		}
		d.auditLog("context.save", map[string]any{"guideSet": p.Guide != nil, "logSet": p.Log != nil})
		return map[string]any{"ok": true}, nil

	case "context.inherited.rule.save":
		var p struct {
			Name string `json:"name"`
			Body string `json:"body"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, errors.New("rule name is required")
		}
		inh := d.inherited()
		if inh == nil {
			return nil, errors.New("inherited context is unavailable")
		}
		if err := inh.SetRule(p.Name, p.Body); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "context.inherited.rule.delete":
		var p struct {
			Name string `json:"name"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		inh := d.inherited()
		if inh == nil {
			return nil, errors.New("inherited context is unavailable")
		}
		if err := inh.DeleteRule(p.Name); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "project.list":
		return d.projectList(ctx), nil

	case "project.create":
		var p struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, errors.New("project name is required")
		}
		if d.settings == nil {
			return nil, errors.New("settings are not available")
		}
		project := settings.Project{ID: d.newProjectID(), Name: p.Name, Path: p.Path}
		d.settings.AddProject(project)
		return map[string]any{
			"project":  ProjectView{ID: project.ID, Name: project.Name, Path: project.Path},
			"projects": d.projectList(ctx),
		}, nil

	case "project.delete":
		var p struct {
			ID string `json:"id"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if d.settings == nil {
			return nil, errors.New("settings are not available")
		}
		d.settings.RemoveProject(p.ID)
		return d.projectList(ctx), nil

	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}

// ProjectView is a project plus the number of threads that belong to it.
type ProjectView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Threads int    `json:"threads"`
}

// threadWorkdir resolves a thread's working directory: its project's path,
// falling back to the environment default when the thread has no project.
// Mirrors the orchestrator's workdirFor so diffs and file trees show the same
// repo the agents work in.
func (d *Dispatcher) threadWorkdir(ctx context.Context, id contracts.ThreadID) string {
	if d.settings == nil {
		return ""
	}
	if st, err := d.engine.State(ctx, id); err == nil && st.ProjectID != "" {
		if project, ok := d.settings.Project(st.ProjectID); ok && project.Path != "" {
			return project.Path
		}
	}
	return d.settings.Workdir()
}

func (d *Dispatcher) projectList(ctx context.Context) []ProjectView {
	counts := map[string]int{}
	if ids, err := d.engine.Threads(ctx); err == nil {
		for _, id := range ids {
			st, err := d.engine.State(ctx, id)
			if err != nil {
				continue
			}
			counts[st.ProjectID]++
		}
	}

	out := []ProjectView{}
	if d.settings != nil {
		for _, p := range d.settings.Projects() {
			out = append(out, ProjectView{ID: p.ID, Name: p.Name, Path: p.Path, Threads: counts[p.ID]})
		}
	}
	return out
}

func (d *Dispatcher) newProjectID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "proj_" + hex.EncodeToString(b[:])
}

func (d *Dispatcher) newThreadID() contracts.ThreadID {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return contracts.ThreadID("th_" + hex.EncodeToString(b[:]))
}

func (d *Dispatcher) nextID() string { return fmt.Sprintf("rpc-%d", d.seq.Add(1)) }

// inherited returns the runner's inherited-context store, if wired.
func (d *Dispatcher) inherited() *contextstore.InheritedContext {
	if d.runner == nil {
		return nil
	}
	return d.runner.Inherited()
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// RoleConfigView is a role's effective runtime for the settings screen.
type RoleConfigView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Variant string `json:"variant"`
	Builtin bool   `json:"builtin"`
}

// SettingsView is the settings screen's payload.
type SettingsView struct {
	Workdir           string           `json:"workdir"`
	AutoMode          bool             `json:"autoMode"`
	ApprovedModels    []string         `json:"approvedModels"`
	Roles             []RoleConfigView `json:"roles"`
	ConfigPath        string           `json:"configPath"`
	StaleAfterMinutes int              `json:"staleAfterMinutes"`
}

type settingsPatch struct {
	Workdir           *string   `json:"workdir"`
	AutoMode          *bool     `json:"autoMode"`
	ApprovedModels    *[]string `json:"approvedModels"`
	StaleAfterMinutes *int      `json:"staleAfterMinutes"`
	Role              *struct {
		ID      string `json:"id"`
		Harness string `json:"harness"`
		Model   string `json:"model"`
		Variant string `json:"variant"`
	} `json:"role"`
	Harness *struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	} `json:"harness"`
}

func (d *Dispatcher) settingsView() SettingsView {
	var view SettingsView
	if d.settings != nil {
		view.Workdir = d.settings.Workdir()
		view.AutoMode = d.settings.AutoMode()
		view.ConfigPath = d.settings.Path()
		view.StaleAfterMinutes = d.settings.StaleAfterMinutes()
		for m := range d.settings.ApprovedModels() {
			view.ApprovedModels = append(view.ApprovedModels, m)
		}
		sort.Strings(view.ApprovedModels)
	}
	for _, role := range d.roles.List() {
		harness, model, variant := role.DefaultHarness, role.DefaultModel, role.DefaultVariant
		if d.settings != nil {
			if cfg, ok := d.settings.RoleConfig(role.ID); ok {
				if cfg.Harness.Valid() {
					harness = cfg.Harness
				}
				if cfg.Model != "" {
					model = cfg.Model
				}
				if cfg.Variant != "" {
					variant = cfg.Variant
				}
			}
		}
		view.Roles = append(view.Roles, RoleConfigView{
			ID:      string(role.ID),
			Name:    role.Name,
			Harness: string(harness),
			Model:   model,
			Variant: variant,
			Builtin: role.Builtin,
		})
	}
	return view
}

// resolveDirPath normalizes a project directory for comparison: absolute, and
// symlink-free when the path exists. A stored project path that no longer
// exists still resolves to its absolute form, so lookup stays stable.
func resolveDirPath(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return filepath.Clean(dir)
}

// graphEventTypes are the only events the agent graph's replay folds. Everything
// else (chat text, shared context, reviews, output) stays out of the payload.
var graphEventTypes = map[contracts.EventType]bool{
	contracts.EvtTaskDispatched:     true,
	contracts.EvtAgentStatusChanged: true,
	contracts.EvtUsageRecorded:      true,
	contracts.EvtMailboxPosted:      true,
	contracts.EvtMessageSent:        true,
}

// graphBodyMax bounds the mailbox body echoed into replay frames. Full bodies
// are already in thread.get's mailbox; replay only needs the edges.
const graphBodyMax = 280

// graphEvent is the slim wire shape of one graph-relevant event. Message-sent
// carries only its author: it marks turn boundaries and its text is not needed.
type graphEvent struct {
	Seq     int64               `json:"seq"`
	Type    contracts.EventType `json:"type"`
	At      time.Time           `json:"at"`
	Payload any                 `json:"payload"`
}

func graphEvents(events []contracts.Event) []graphEvent {
	out := make([]graphEvent, 0, len(events))
	for _, ev := range events {
		if !graphEventTypes[ev.Type()] {
			continue
		}
		var payload any = ev.Payload
		switch p := ev.Payload.(type) {
		case contracts.MailboxPosted:
			p.Body = clipRunes(p.Body, graphBodyMax)
			payload = p
		case contracts.MessageSent:
			payload = struct {
				Author string `json:"author"`
			}{p.Author}
		}
		out = append(out, graphEvent{Seq: ev.Seq, Type: ev.Type(), At: ev.At, Payload: payload})
	}
	return out
}

func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
