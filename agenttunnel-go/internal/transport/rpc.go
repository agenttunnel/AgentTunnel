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
	"github.com/agenttunnel/agenttunnel-go/internal/update"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
	"github.com/agenttunnel/agenttunnel-go/internal/worktree"
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
	Updater      *update.Updater
	// Relay drives the outbound relay client. Nil when the daemon has no relay.
	Relay RelayControl
}

// RelayControl is the outbound relay client as the RPC surface drives it.
// internal/relay implements it, so the transport never imports the relay.
type RelayControl interface {
	// Status reports the relay configuration and whether a daemon socket is up.
	Status() RelayStatus
	// Enable validates a relay URL, saves it, and connects. Disable stops it.
	Enable(url string) error
	Disable() error
	// PairStart mints a pairing code the relay will accept once.
	PairStart(ctx context.Context) (PairOffer, error)
	// Devices lists the phones paired with this instance.
	Devices() []RelayDevice
	// Unpair revokes one device, or every device when all is set.
	Unpair(ctx context.Context, clientID string, all bool) error
}

// RelayStatus is the relay configuration plus live connection state.
type RelayStatus struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	Connected bool   `json:"connected"`
	Instance  string `json:"instance,omitempty"`
}

// PairOffer is a pairing code and the time the relay stops accepting it.
type PairOffer struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

// RelayDevice is a phone paired with this instance. The token lives only in
// the relay and on the phone; this record is the desktop's view of the pairing.
type RelayDevice struct {
	ClientID string    `json:"clientId"`
	Name     string    `json:"name"`
	PairedAt time.Time `json:"pairedAt"`
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
	updater  *update.Updater
	relay    RelayControl
	seq      atomic.Int64

	// turns gates one running turn per thread and lets clients stop it.
	turnsMu sync.Mutex
	turns   map[contracts.ThreadID]context.CancelFunc

	// worktreeFailures holds, per thread, the reason its last first-worktree cut
	// failed. It only dedupes the audit line: every turn still retries the cut.
	worktreeMu       sync.Mutex
	worktreeFailures map[contracts.ThreadID]string

	// shellStarted, when set by a test, runs once a thread.runCommand child is
	// live. Nil in production.
	shellStarted func()

	// questions holds the blocked `tool ask` calls, keyed by thread and question.
	questions *questionBroker
}

// NewDispatcher constructs a dispatcher.
func NewDispatcher(d DispatcherDeps) *Dispatcher {
	return &Dispatcher{
		questions: newQuestionBroker(),
		engine:    d.Engine,
		runner:    d.Runner,
		orch:      d.Orchestrator,
		ledger:    d.Ledger,
		logs:      d.Logs,
		roles:     d.Roles,
		registry:  d.Registry,
		settings:  d.Settings,
		models:    d.Models,
		steering:  d.Steering,
		audit:     d.Audit,
		updater:   d.Updater,
		relay:     d.Relay,
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
	// ParentThreadID marks a subthread and names the thread that started it.
	ParentThreadID contracts.ThreadID     `json:"parentThreadId,omitempty"`
	Worktree       *contracts.WorktreeRef `json:"worktree,omitempty"`

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
		// Worktree cuts the thread's worktree up front instead of on its first turn.
		Worktree bool `json:"worktree"`
	}
	threadParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	turnParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Text     string             `json:"text"`
		// Attachments are workdir-relative files or folders the composer attached.
		// They reach the agent's prompt, not the transcript.
		Attachments []string `json:"attachments,omitempty"`
	}
	setBoolParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Enabled  bool               `json:"enabled"`
	}
	setLeadsParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Count    int                `json:"count"`
	}
	setLeadRuntimeParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Index    int                `json:"index"`
		Harness  contracts.Harness  `json:"harness,omitempty"`
		Model    string             `json:"model,omitempty"`
		Variant  string             `json:"variant,omitempty"`
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

// runTurn starts a user turn: it claims the thread's turn slot first, so a busy
// thread is refused before cmd is recorded, then records cmd and runs the lead
// orchestrators on text. A stop from the client is reported as ok, not an error.
func (d *Dispatcher) runTurn(ctx context.Context, threadID contracts.ThreadID, text string, cmd contracts.Command) (any, error) {
	turnCtx, done, err := d.beginTurn(ctx, threadID)
	if err != nil {
		return nil, err
	}
	defer done()
	// The turn-start gate runs before any agent: the thread's worktree must exist
	// (cut on the first turn, restored if it vanished) or the turn does not run.
	if err := d.ensureWorktree(ctx, threadID); err != nil {
		return nil, err
	}
	if err := d.engine.Dispatch(ctx, cmd); err != nil {
		return nil, err
	}
	if err := d.orch.RunTurn(turnCtx, threadID, text); err != nil {
		if turnCtx.Err() != nil {
			return map[string]any{"ok": true, "stopped": true}, nil
		}
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// promptWithAttachments appends the composer's attached files and folders to
// a prompt. The transcript keeps the typed text; only the agent sees the
// context block. Paths resolve inside the thread's workdir.
func (d *Dispatcher) promptWithAttachments(ctx context.Context, id contracts.ThreadID, text string, attachments []string) (string, error) {
	if len(attachments) == 0 {
		return text, nil
	}
	wd := d.threadWorkdir(ctx, id)
	if wd == "" {
		return "", errors.New("thread has no working directory for attachments")
	}
	block, err := files.AttachmentBlock(wd, attachments)
	if err != nil {
		return "", err
	}
	return text + "\n\n" + block, nil
}

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
		// Nothing is left to hear an answer once the turn is over.
		d.closeOpenQuestions(threadID, "turn ended")
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
		var wt *contracts.WorktreeRef
		if p.Worktree && p.ProjectID != "" {
			wt = d.createWorktreeRef(ctx, p.ProjectID, p.Title, id)
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdThreadCreate, ThreadID: id,
			Title: p.Title, ProjectID: p.ProjectID, Worktree: wt,
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
				ParentThreadID: st.ParentThreadID, Worktree: st.Worktree,
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
		prompt, err := d.promptWithAttachments(ctx, p.ThreadID, p.Text, p.Attachments)
		if err != nil {
			return nil, err
		}
		return d.runTurn(ctx, p.ThreadID, prompt, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdTurnStart, ThreadID: p.ThreadID, Text: p.Text,
		})

	case "review.submit":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
		}
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
		var pending []contracts.ReviewCommentAdded
		var ids []string
		for _, c := range st.ReviewComments {
			if c.Submitted {
				continue
			}
			pending = append(pending, c.ReviewCommentAdded)
			ids = append(ids, c.ID)
		}
		if len(ids) == 0 {
			return nil, errors.New("no pending review comments")
		}
		text := domain.ReviewRequest(pending)
		return d.runTurn(ctx, p.ThreadID, text, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdReviewSubmit, ThreadID: p.ThreadID, Text: text,
			ReviewSubmit: &contracts.ReviewCommentsSubmitted{CommentIDs: ids},
		})

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
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			// RemoveWorktree deletes the thread's own worktree with it. Off by
			// default: the worktree may hold work the user still wants.
			RemoveWorktree bool `json:"removeWorktree"`
		}
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
		// A refused removal (dirty worktree, running turn) aborts the delete.
		if p.RemoveWorktree && st.Worktree != nil && !st.Worktree.Inherited {
			if _, err := d.worktreeRemove(ctx, st.ProjectID, st.Worktree.Path, false); err != nil {
				return nil, err
			}
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
		prompt, err := d.promptWithAttachments(ctx, p.ThreadID, p.Text, p.Attachments)
		if err != nil {
			return nil, err
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
		d.steering.Post(p.ThreadID, prompt)
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

	case "thread.setLeadRuntime":
		var p setLeadRuntimeParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdLeadRuntimeSet, ThreadID: p.ThreadID,
			LeadRuntime: &contracts.LeadRuntimeSet{
				Index:   p.Index,
				Runtime: contracts.LeadRuntime{Harness: p.Harness, Model: p.Model, Variant: p.Variant},
			},
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "question.answer":
		var p struct {
			ThreadID   contracts.ThreadID `json:"threadId"`
			QuestionID string             `json:"questionId"`
			Selected   []string           `json:"selected"`
			Text       string             `json:"text"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if err := d.resolveQuestion(ctx, p.ThreadID, p.QuestionID, contracts.QuestionClosed{
			Answered: true, Selected: p.Selected, Text: p.Text,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "question.dismiss":
		var p struct {
			ThreadID   contracts.ThreadID `json:"threadId"`
			QuestionID string             `json:"questionId"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if err := d.resolveQuestion(ctx, p.ThreadID, p.QuestionID, contracts.QuestionClosed{
			Reason: contracts.ReasonQuestionDeclined,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "thread.setLeads":
		var p setLeadsParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdLeadsSet, ThreadID: p.ThreadID, Leads: &p.Count,
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

	case "stats.harnesses":
		// No params is the whole environment; a threadId or projectId narrows it.
		var p struct {
			ThreadID  contracts.ThreadID `json:"threadId"`
			ProjectID string             `json:"projectId"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.engine.HarnessStats(ctx, domain.HarnessScope{ThreadID: p.ThreadID, ProjectID: p.ProjectID})

	case "update.check":
		if d.updater == nil {
			return nil, errors.New("updates are unavailable")
		}
		return d.updater.Check(ctx)

	case "update.apply":
		if d.updater == nil {
			return nil, errors.New("updates are unavailable")
		}
		return d.updater.Apply(ctx)

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

	case "files.search":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Workdir  string             `json:"workdir"`
			Query    string             `json:"query"`
			Limit    int                `json:"limit"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		wd := p.Workdir
		if wd == "" {
			wd = d.threadWorkdir(ctx, p.ThreadID)
		}
		matches, err := files.Search(ctx, wd, p.Query, p.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"matches": matches}, nil

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
			EndLine  int                `json:"endLine"`
			Side     string             `json:"side"`
			Body     string             `json:"body"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		comment := &contracts.ReviewCommentAdded{
			ID:        d.nextID(),
			File:      p.File,
			Line:      p.Line,
			EndLine:   p.EndLine,
			Side:      p.Side,
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

	case "worktree.list":
		var p struct {
			ProjectID string `json:"projectId"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.worktreeList(ctx, p.ProjectID)

	case "worktree.create":
		var p threadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.worktreeCreate(ctx, p.ThreadID)

	case "worktree.remove":
		var p struct {
			ProjectID string `json:"projectId"`
			Path      string `json:"path"`
			Force     bool   `json:"force"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.worktreeRemove(ctx, p.ProjectID, p.Path, p.Force)

	case "worktree.prune":
		var p struct {
			ProjectID string `json:"projectId"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.worktreePrune(ctx, p.ProjectID)

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

	case "relay.status":
		return d.relayControl().Status(), nil

	case "relay.enable":
		var p struct {
			URL string `json:"url"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		rc := d.relayControl()
		if err := rc.Enable(p.URL); err != nil {
			return nil, err
		}
		return rc.Status(), nil

	case "relay.disable":
		rc := d.relayControl()
		if err := rc.Disable(); err != nil {
			return nil, err
		}
		return rc.Status(), nil

	case "relay.pair.start":
		return d.relayControl().PairStart(ctx)

	case "relay.devices.list":
		return map[string]any{"devices": d.relayControl().Devices()}, nil

	case "relay.unpair":
		var p struct {
			ClientID string `json:"clientId"`
			All      bool   `json:"all"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if p.ClientID == "" && !p.All {
			return nil, errors.New("clientId or all is required")
		}
		rc := d.relayControl()
		if err := rc.Unpair(ctx, p.ClientID, p.All); err != nil {
			return nil, err
		}
		return map[string]any{"devices": rc.Devices()}, nil

	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}

// relayControl returns the relay client, or one that refuses every call when
// the daemon was built without a relay.
func (d *Dispatcher) relayControl() RelayControl {
	if d.relay == nil {
		return noRelay{}
	}
	return d.relay
}

// noRelay answers the relay RPCs for a daemon with no relay client wired in.
type noRelay struct{}

var errNoRelay = errors.New("relay is not available")

func (noRelay) Status() RelayStatus                          { return RelayStatus{} }
func (noRelay) Enable(string) error                          { return errNoRelay }
func (noRelay) Disable() error                               { return errNoRelay }
func (noRelay) PairStart(context.Context) (PairOffer, error) { return PairOffer{}, errNoRelay }
func (noRelay) Devices() []RelayDevice                       { return []RelayDevice{} }
func (noRelay) Unpair(context.Context, string, bool) error   { return errNoRelay }

// ProjectView is a project plus the number of threads that belong to it.
type ProjectView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Threads int    `json:"threads"`
}

// threadWorkdir resolves a thread's working directory: its worktree when it has
// one on disk, otherwise its project's path. Mirrors the orchestrator's
// workdirFor so diffs and file trees show the same checkout the agents use.
func (d *Dispatcher) threadWorkdir(ctx context.Context, id contracts.ThreadID) string {
	if d.settings == nil {
		return ""
	}
	base := d.settings.Workdir()
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return base
	}
	if st.ProjectID != "" {
		if project, ok := d.settings.Project(st.ProjectID); ok && project.Path != "" {
			base = project.Path
		}
	}
	return worktree.Resolve(st.Worktree, base)
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
