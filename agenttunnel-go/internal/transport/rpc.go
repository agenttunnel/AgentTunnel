package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/actionlog"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/files"
	"github.com/agenttunnel/agenttunnel-go/internal/gitdiff"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
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
)

const defaultStaleAfterSeconds = 30 * 60

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
		if err := d.orch.RunTurn(ctx, p.ThreadID, p.Text); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

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

	case "agents.logs":
		var p agentParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		// The on-disk log is the superset: it holds everything the live buffer
		// does, plus anything written before a restart. Read it first and fall
		// back to the buffer only when nothing has been persisted yet.
		entries, err := d.logs.ReadFile(p.AgentID)
		if err != nil || len(entries) == 0 {
			entries = d.logs.Entries(p.AgentID)
		}
		return entries, nil

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
