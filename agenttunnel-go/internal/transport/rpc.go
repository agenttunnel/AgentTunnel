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
	"github.com/agenttunnel/agenttunnel-go/internal/attachments"
	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/deps"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/fileedit"
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
	// Version is the release version embedded at link time, reported by app.info.
	Version string
	// HarnessUpdates checks and upgrades the provider CLIs themselves. Nil
	// reports none; this is distinct from Updater, which updates AgentTunnel.
	HarnessUpdates *providers.HarnessUpdater
	// Limits reports subscription usage left per harness. Nil reports none.
	Limits *providers.LimitsProvider
	// Relay drives the outbound relay client. Nil when the daemon has no relay.
	Relay RelayControl
	// Attachments stores image uploads. Nil refuses image-bearing turns.
	Attachments *attachments.Store
	// BaseCtx is the daemon's lifetime context. Detached turns run against it,
	// so a client socket closing cannot cancel them; only the daemon going
	// away or thread.turn.stop can. Nil falls back to context.Background.
	BaseCtx context.Context
	// Shutdown triggers a graceful daemon shutdown for the daemon.shutdown RPC.
	// The assembly sets it; nil makes the RPC a no-op acknowledgement.
	Shutdown func()
	// Remote drives WhatsApp connectivity for the Remote section. Nil reports it
	// unavailable.
	Remote RemoteControl
	// Clock and StatusTick drive the per-turn workflow status report. Nil uses
	// real time; tests inject a controllable ticker and clock.
	Clock      domain.Clock
	StatusTick StatusTicker
}

// StatusTicker returns a tick channel and a stop function for a period. It is
// the seam tests use to fire status reports without sleeping.
type StatusTicker func(time.Duration) (<-chan time.Time, func())

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
	// StartTunnel serves a relay on the LAN inside this daemon and points the
	// relay client at it. It returns once the daemon socket is up. StopTunnel
	// closes it; TunnelStatus reports it.
	StartTunnel(ctx context.Context) (TunnelStatus, error)
	StopTunnel() error
	TunnelStatus() TunnelStatus
}

// TunnelStatus describes the in-app relay. LANURL is the address a phone dials
// and is empty when the tunnel is off.
type TunnelStatus struct {
	Listening bool   `json:"listening"`
	Port      int    `json:"port,omitempty"`
	LANURL    string `json:"lanUrl,omitempty"`
}

// RelayStatus is the relay configuration plus live connection state.
type RelayStatus struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	Connected bool   `json:"connected"`
	Instance  string `json:"instance,omitempty"`
	// LANHost is this machine's private IPv4 address, for the pairing QR when
	// the saved relay URL points at loopback. Empty when there is none.
	LANHost string `json:"lanHost,omitempty"`
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
	engine         *domain.Engine
	runner         *orchestrator.Runner
	orch           *orchestrator.Orchestrator
	ledger         *usage.Ledger
	logs           *logstore.Store
	roles          *contracts.RoleRegistry
	registry       *providers.Registry
	settings       *settings.Store
	models         *models.Service
	steering       *steering.Box
	audit          actionlog.Func
	updater        *update.Updater
	version        string
	harnessUpdates *providers.HarnessUpdater
	limits         *providers.LimitsProvider
	relay          RelayControl
	remote         RemoteControl
	master         *orchestrator.Master
	attach         *attachments.Store
	prBoard        *scm.Board
	dirtyBoard     *scm.DirtyBoard
	baseCtx        context.Context
	shutdown       func()
	seq            atomic.Int64

	// remoteNotifier broadcasts Remote section changes. Set after the server
	// exists, since the dispatcher is built first.
	remoteNotifier func(RemoteStatus)
	// clock and statusTick drive the per-turn workflow status report.
	clock      domain.Clock
	statusTick StatusTicker

	// turns gates one running turn per thread and lets clients stop it.
	turnsMu sync.Mutex
	turns   map[contracts.ThreadID]turnRun

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
	base := d.BaseCtx
	if base == nil {
		base = context.Background()
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.StatusTick == nil {
		d.StatusTick = realStatusTick
	}
	disp := &Dispatcher{
		questions:      newQuestionBroker(),
		baseCtx:        base,
		shutdown:       d.Shutdown,
		engine:         d.Engine,
		runner:         d.Runner,
		orch:           d.Orchestrator,
		ledger:         d.Ledger,
		logs:           d.Logs,
		roles:          d.Roles,
		registry:       d.Registry,
		settings:       d.Settings,
		models:         d.Models,
		steering:       d.Steering,
		audit:          d.Audit,
		updater:        d.Updater,
		version:        d.Version,
		harnessUpdates: d.HarnessUpdates,
		limits:         d.Limits,
		relay:          d.Relay,
		remote:         d.Remote,
		attach:         d.Attachments,
		clock:          d.Clock,
		statusTick:     d.StatusTick,
		prBoard:        scm.NewBoard(scm.Fetch, prBoardTTL),
		dirtyBoard:     scm.NewDirtyBoard(scm.Dirty, dirtyBoardTTL),
	}
	disp.master = orchestrator.NewMaster(orchestrator.MasterDeps{
		Runner: d.Runner, Engine: d.Engine, Settings: d.Settings, Models: d.Models,
	})
	return disp
}

// prBoardTTL bounds how stale a thread board's PR state may be. Each refresh
// is one gh call per worktree, so the board never polls faster than this.
const prBoardTTL = 60 * time.Second

// dirtyBoardTTL matches the board's 20s poll: uncommitted work should move
// columns about as fast as the poll can show it. git status is local and cheap.
const dirtyBoardTTL = 20 * time.Second

// ThreadPR is the PR a thread's worktree branch has, as the thread board shows it.
type ThreadPR struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"` // open | merged | closed
	Draft          bool   `json:"draft"`
	ReviewDecision string `json:"reviewDecision"` // approved | changes_requested | review_required | none
}

// ThreadState is what the thread board knows about one thread's worktree.
type ThreadState struct {
	PR    *ThreadPR `json:"pr,omitempty"`
	Dirty bool      `json:"dirty"`
}

// threadStates maps each thread with a worktree to its branch PR and dirty flag.
// workdir returns the thread's worktree directory, or "" when it has none;
// peekPR and peekDirty read the cached forge snapshot and working-tree state for
// a directory, ok=false until the first fetch lands. Unknown state reads as no
// PR and clean, so the thread lands in the PR column rather than erroring.
// Threads without a worktree are omitted.
func threadStates(
	ids []contracts.ThreadID,
	workdir func(contracts.ThreadID) string,
	peekPR func(dir string) (scm.Snapshot, bool),
	peekDirty func(dir string) (dirty bool, ok bool),
) map[contracts.ThreadID]ThreadState {
	out := make(map[contracts.ThreadID]ThreadState, len(ids))
	for _, id := range ids {
		wd := workdir(id)
		if wd == "" {
			continue
		}
		var st ThreadState
		if dirty, ok := peekDirty(wd); ok {
			st.Dirty = dirty
		}
		if snap, ok := peekPR(wd); ok && snap.Request != nil {
			r := snap.Request
			st.PR = &ThreadPR{Number: r.Number, URL: r.URL, State: r.State, Draft: r.Draft, ReviewDecision: r.Review}
		}
		out[id] = st
	}
	return out
}

// maxSummaryPromptRunes bounds the searchable prompt in the thread list: the
// openers are short, and the list is shipped whole on every recency change.
const maxSummaryPromptRunes = 200

// firstUserPrompt returns the thread's first human message, truncated to the
// list-search budget. Threads started only by agents have none.
func firstUserPrompt(st *domain.ThreadState) string {
	for _, m := range st.Messages {
		if m.Author != "user" {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		r := []rune(text)
		if len(r) > maxSummaryPromptRunes {
			return string(r[:maxSummaryPromptRunes])
		}
		return text
	}
	return ""
}

// ThreadSummary is the list-row projection of a thread.
type ThreadSummary struct {
	ID        contracts.ThreadID `json:"id"`
	Title     string             `json:"title"`
	ProjectID string             `json:"projectId,omitempty"`
	// FirstPrompt is the first human message, truncated so list search can
	// match against what the thread was actually asked to do.
	FirstPrompt string    `json:"firstPrompt,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Messages    int       `json:"messages"`
	Agents      int       `json:"agents"`
	AutoMode    bool      `json:"autoMode"`
	Archived    bool      `json:"archived"`
	// ParentThreadID marks a subthread and names the thread that started it.
	ParentThreadID contracts.ThreadID `json:"parentThreadId,omitempty"`
	// Owner records who started the thread; absent means user (pre-change logs).
	Owner    contracts.ThreadOwner  `json:"owner,omitempty"`
	Worktree *contracts.WorktreeRef `json:"worktree,omitempty"`
	Draft    bool                   `json:"draft,omitempty"`
	Issue    *contracts.IssueRef    `json:"issue,omitempty"`

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
		// Draft creates a thread that waits for its first turn. Worktree is
		// ignored: the first turn cuts it.
		Draft       bool                `json:"draft,omitempty"`
		Description string              `json:"description,omitempty"`
		Issue       *contracts.IssueRef `json:"issue,omitempty"`
	}
	forkThreadParams struct {
		ThreadID  contracts.ThreadID `json:"threadId"`
		MessageID string             `json:"messageId"`
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
		// Images are attachment ids minted by a prior POST /attachments
		// upload. They render in the transcript and reach harnesses that
		// accept image input.
		Images []string `json:"images,omitempty"`
		// Detach runs the turn against the daemon's lifetime context and replies
		// at once, so a client (agenttunnel run) can exit while the work continues.
		Detach bool `json:"detach,omitempty"`
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
	setRoleRuntimeParams struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Role     string             `json:"role"`
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
// thread is refused before cmd is recorded, and the turn's image attachments
// must resolve to readable files before cmd is recorded too. It then records
// cmd and runs the lead orchestrators on text. A stop from the client is
// reported as ok, not an error.
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
	// Attachment files resolve before the turn is recorded: a message that
	// names an image the harness cannot read would otherwise sit in the
	// transcript as if it had been delivered.
	inputs, err := d.imageInputs(threadID, cmd.Images)
	if err != nil {
		return nil, err
	}
	if err := d.engine.Dispatch(ctx, cmd); err != nil {
		return nil, err
	}
	// Status reports run beside the orchestrator, not inside its pass loop, so a
	// long pass still shows progress. The stop func joins the goroutine before
	// the turn slot is released.
	stopReports := d.startStatusReports(turnCtx, threadID)
	defer stopReports()

	if err := d.orch.RunTurn(turnCtx, threadID, text, inputs...); err != nil {
		if turnCtx.Err() != nil {
			return map[string]any{"ok": true, "stopped": true}, nil
		}
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// resolveImages decodes uploaded attachment ids into their stored meta, so a
// turn can record them in the transcript. Raw ids are never forwarded: only
// pairs the store can vouch for pass through.
func (d *Dispatcher) resolveImages(threadID contracts.ThreadID, ids []string) ([]contracts.ImageRef, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > contracts.MaxTurnImages {
		return nil, fmt.Errorf("a turn carries at most %d images", contracts.MaxTurnImages)
	}
	if d.attach == nil {
		return nil, errors.New("image attachments are unavailable")
	}
	var refs []contracts.ImageRef
	for _, id := range ids {
		if !contracts.ValidImageID(id) {
			return nil, fmt.Errorf("%q is not an attachment id", id)
		}
		ref, err := d.attach.Meta(threadID, id)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// imageInputs maps recorded refs to the files the harnesses read. The store
// resolves each path from the thread's own directory. A ref that cannot be
// resolved is an error, never a silent drop: the transcript records the image
// the moment the turn starts, so it must be readable by then.
func (d *Dispatcher) imageInputs(threadID contracts.ThreadID, refs []contracts.ImageRef) ([]providers.ImageInput, error) {
	if d.attach == nil || len(refs) == 0 {
		return nil, nil
	}
	var inputs []providers.ImageInput
	for _, r := range refs {
		path := d.attach.Path(threadID, r.ID)
		if path == "" {
			return nil, fmt.Errorf("attachment %s is not readable", r.ID)
		}
		inputs = append(inputs, providers.ImageInput{Path: path, Mime: r.Mime, Name: r.Name})
	}
	return inputs, nil
}

// startDetachedTurn claims the thread's turn slot against the daemon's lifetime
// context and runs the turn in the background, replying at once. The caller's
// socket closing cannot cancel it — only thread.turn.stop or daemon shutdown
// can. The slot is released when the goroutine finishes, so a stopped turn
// keeps it until it unwinds, exactly like a foreground turn.
func (d *Dispatcher) startDetachedTurn(ctx context.Context, threadID contracts.ThreadID, text string, cmd contracts.Command) (any, error) {
	if d.orch == nil || d.engine == nil {
		return nil, errors.New("detached turns are unavailable")
	}
	// A caller with no daemon context (a bare dispatcher in a test) still gets
	// a turn that outlives its socket. A daemon context that is already
	// cancelled is respected: shutdown must not start new work.
	base := d.baseCtx
	if base == nil {
		base = context.WithoutCancel(ctx)
	}
	turnCtx, done, err := d.beginTurn(base, threadID)
	if err != nil {
		return nil, err
	}
	// Image attachments must resolve before cmd is recorded: a detached
	// turn that cannot deliver them leaves no transcript entry either.
	inputs, err := d.imageInputs(threadID, cmd.Images)
	if err != nil {
		done()
		return nil, err
	}
	go func() {
		defer done()
		if err := d.ensureWorktree(base, threadID); err != nil {
			d.auditLog("turn.detached.failed", map[string]any{"threadId": string(threadID), "error": err.Error()})
			return
		}
		if err := d.engine.Dispatch(base, cmd); err != nil {
			d.auditLog("turn.detached.failed", map[string]any{"threadId": string(threadID), "error": err.Error()})
			return
		}
		if err := d.orch.RunTurn(turnCtx, threadID, text, inputs...); err != nil {
			if turnCtx.Err() != nil {
				d.auditLog("turn.detached.stopped", map[string]any{"threadId": string(threadID)})
				return
			}
			d.auditLog("turn.detached.failed", map[string]any{"threadId": string(threadID), "error": err.Error()})
		}
	}()
	return map[string]any{"ok": true, "detached": true, "threadId": string(threadID)}, nil
}

// Shutdown asks the daemon assembly to shut down gracefully. It is called by
// the server after the daemon.shutdown reply is written.
func (d *Dispatcher) Shutdown() {
	if d.shutdown != nil {
		d.shutdown()
	}
}

// realStatusTick is the production status ticker.
func realStatusTick(period time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(period)
	return t.C, t.Stop
}

// startStatusReports starts a thread's per-turn status ticker. A thread with no
// configured period starts nothing. The returned stop function ends the loop and
// waits for it to exit, so no report can land after the turn is done.
func (d *Dispatcher) startStatusReports(ctx context.Context, threadID contracts.ThreadID) func() {
	if d.engine == nil || d.clock == nil || d.statusTick == nil {
		return func() {}
	}
	period := d.statusPeriod(ctx, threadID)
	if period <= 0 {
		return func() {}
	}
	tick, stopTick := d.statusTick(time.Duration(period) * time.Second)
	reportCtx, cancel := context.WithCancel(ctx)
	start := d.clock()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-reportCtx.Done():
				return
			case _, ok := <-tick:
				if !ok {
					return
				}
				d.postStatusReport(reportCtx, threadID, start)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			stopTick()
			<-finished
		})
	}
}

// statusPeriod reads the thread's configured report period.
func (d *Dispatcher) statusPeriod(ctx context.Context, threadID contracts.ThreadID) int {
	st, err := d.engine.State(ctx, threadID)
	if err != nil || st == nil {
		return 0
	}
	return st.StatusPeriodSeconds
}

// postStatusReport records one workflow status message. It is authored by
// AuthorStatus, never "user", so it does not open a turn or move the spend
// window; the message joins the running turn in the transcript.
func (d *Dispatcher) postStatusReport(ctx context.Context, threadID contracts.ThreadID, start time.Time) {
	st, err := d.engine.State(ctx, threadID)
	if err != nil || st == nil || !st.Exists || st.Deleted {
		return
	}
	text := statusReportText(st, d.clock().Sub(start))
	_ = d.engine.Dispatch(ctx, contracts.Command{
		ID:       d.nextID(),
		Type:     contracts.CmdMessageAppend,
		ThreadID: threadID,
		Message:  &contracts.MessageSent{MessageID: d.nextID(), Author: contracts.AuthorStatus, Text: text},
	})
}

// statusReportText renders a workflow status: what is done, what is still open,
// and what needs the human's attention. Tasks and agents are read in one pass
// each; the report is built in a single builder.
func statusReportText(st *domain.ThreadState, elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	var (
		doneLines, todoLines, worthLines []string
		doneTasks                        int
		settled                          = map[contracts.AgentStatus]int{}
	)
	for _, t := range st.Tasks {
		label := strings.TrimSpace(t.Title)
		if label == "" {
			label = t.Name
		}
		if t.Status == contracts.TaskDone {
			doneTasks++
			doneLines = append(doneLines, "- task "+label)
			continue
		}
		todoLines = append(todoLines, fmt.Sprintf("- task %s (%s)", label, t.Status))
	}
	for _, id := range st.AgentOrder {
		a := st.Agents[id]
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = string(id)
		}
		switch a.Status {
		case contracts.AgentSucceeded:
			settled[a.Status]++
			doneLines = append(doneLines, "- agent "+name+" succeeded")
		case contracts.AgentFailed, contracts.AgentCancelled:
			settled[a.Status]++
			line := "- agent " + name + " " + string(a.Status)
			if reason := oneLine(a.Reason); reason != "" {
				line += ": " + reason
			}
			worthLines = append(worthLines, line)
		default:
			role := string(a.Role)
			if role == "" {
				role = "agent"
			}
			todoLines = append(todoLines, fmt.Sprintf("- agent %s (%s): %s", name, role, a.Status))
		}
	}
	for _, q := range st.Questions {
		if q.Status == domain.QuestionPending {
			worthLines = append(worthLines, "- question waiting: "+oneLine(q.Question))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Workflow status · %s elapsed\n", elapsed.Truncate(time.Second))
	writeReportSection(&b, "Done so far", doneLines)
	writeReportSection(&b, "Still to do", todoLines)
	writeReportSection(&b, "Worth knowing", worthLines)
	if len(settled) > 0 {
		total := 0
		var parts []string
		for _, status := range []contracts.AgentStatus{contracts.AgentSucceeded, contracts.AgentFailed, contracts.AgentCancelled} {
			if n := settled[status]; n > 0 {
				total += n
				parts = append(parts, fmt.Sprintf("%d %s", n, status))
			}
		}
		fmt.Fprintf(&b, "Settled: %d (%s)\n", total, strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "Tasks: %d/%d done", doneTasks, len(st.Tasks))
	return b.String()
}

// writeReportSection writes a titled list, skipping the section when empty.
func writeReportSection(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	b.WriteString(title + ":\n")
	for _, line := range lines {
		b.WriteString(line + "\n")
	}
}

// oneLine collapses whitespace so a multi-line reason or question stays on one
// report line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
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
		d.turns = map[contracts.ThreadID]turnRun{}
	}
	if _, running := d.turns[threadID]; running {
		return nil, nil, fmt.Errorf("a turn is already running on thread %s", threadID)
	}
	turnCtx, cancel := context.WithCancel(ctx)
	d.turns[threadID] = turnRun{ctx: turnCtx, cancel: cancel}
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
	run, running := d.turns[threadID]
	d.turnsMu.Unlock()
	if running {
		run.cancel()
	}
	return running
}

// turnRun is a thread's claimed turn slot: its context ends when the turn is
// stopped or finishes, and cancel ends it.
type turnRun struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// recordShell writes a finished command into the thread log, so the command and
// its output show in the chat. Author "shell" keeps it from counting as a turn.
func (d *Dispatcher) recordShell(ctx context.Context, threadID contracts.ThreadID, command string, res shellResult) error {
	return d.engine.Dispatch(ctx, contracts.Command{
		ID:       d.nextID(),
		Type:     contracts.CmdMessageAppend,
		ThreadID: threadID,
		Message:  &contracts.MessageSent{MessageID: d.nextID(), Author: "shell", Text: shellMessage(command, res)},
	})
}

// toolContext returns a context for a tool call's shell: it ends when the
// thread's running turn is stopped, or when the calling process goes away. With
// no turn running on the thread, only the caller's disconnect ends it.
func (d *Dispatcher) toolContext(reqCtx context.Context, threadID contracts.ThreadID) (context.Context, context.CancelFunc) {
	base := reqCtx
	d.turnsMu.Lock()
	if run, running := d.turns[threadID]; running {
		base = run.ctx
	}
	d.turnsMu.Unlock()
	ctx, cancel := context.WithCancel(base)
	stop := context.AfterFunc(reqCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
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
		imageSupport := make(map[string]string, len(contracts.AllHarnesses()))
		for _, h := range contracts.AllHarnesses() {
			if d.settings == nil || d.settings.HarnessEnabled(h) {
				enabled = append(enabled, h)
			}
			if d.models != nil {
				modelsByHarness[string(h)] = d.models.List(h)
			}
			imageSupport[string(h)] = d.registry.ImageSupport(h).String()
		}
		return map[string]any{
			"all":          contracts.AllHarnesses(),
			"available":    d.registry.Available(),
			"enabled":      enabled,
			"models":       modelsByHarness,
			"imageSupport": imageSupport,
		}, nil

	case "harness.updates":
		var p struct {
			Refresh bool `json:"refresh"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if d.harnessUpdates == nil {
			return providers.UpdateReport{CheckedAt: time.Now(), Harnesses: []providers.HarnessUpdate{}}, nil
		}
		return d.harnessUpdates.Check(ctx, p.Refresh), nil

	case "harness.update":
		var p struct {
			Harnesses []string `json:"harnesses"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if d.harnessUpdates == nil {
			return nil, errors.New("harness updates are unavailable")
		}
		only := make([]contracts.Harness, 0, len(p.Harnesses))
		for _, name := range p.Harnesses {
			h := contracts.Harness(name)
			if !h.Valid() {
				return nil, fmt.Errorf("unknown harness %q", name)
			}
			only = append(only, h)
		}
		return d.harnessUpdates.Update(ctx, only)

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
		// The desktop's `agenttunnel-desktop <dir>` flow: resolve the directory to
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
			Owner: contracts.ThreadOwnerUser,
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
		if p.Issue != nil {
			if strings.TrimSpace(p.Issue.URL) == "" {
				return nil, errors.New("issue url is required")
			}
			if existing, found, err := d.issueThread(ctx, p.ProjectID, p.Issue.URL); err != nil {
				return nil, err
			} else if found {
				return nil, fmt.Errorf("issue already has thread %s", existing)
			}
		}
		id := d.newThreadID()
		var wt *contracts.WorktreeRef
		if p.Worktree && p.ProjectID != "" && !p.Draft {
			wt = d.createWorktreeRef(ctx, p.ProjectID, p.Title, id)
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdThreadCreate, ThreadID: id,
			Title: p.Title, ProjectID: p.ProjectID, Worktree: wt,
			Draft: p.Draft, Description: p.Description, Issue: p.Issue,
			Owner: contracts.ThreadOwnerUser,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"threadId": id}, nil

	case "thread.fork":
		var p forkThreadParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.forkThread(ctx, p)

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
				FirstPrompt: firstUserPrompt(st),
				Messages:    len(st.Messages), Agents: len(st.Agents), AutoMode: st.AutoMode, Archived: st.Archived,
				ParentThreadID: st.ParentThreadID, Owner: st.Owner, Worktree: st.Worktree,
				Draft: st.Draft, Issue: st.Issue,
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
		st, err := d.engine.State(ctx, p.ThreadID)
		if err != nil {
			return nil, err
		}
		if !st.Exists {
			return nil, fmt.Errorf("thread %q does not exist", p.ThreadID)
		}
		// A draft's first turn may omit the text: its title and description are
		// the prompt, so every client starts a draft the same way.
		text := p.Text
		if strings.TrimSpace(text) == "" && st.Draft {
			text = draftPrompt(st)
		}
		if strings.TrimSpace(text) == "" {
			return nil, errors.New("turn text is required")
		}
		prompt, err := d.promptWithAttachments(ctx, p.ThreadID, text, p.Attachments)
		if err != nil {
			return nil, err
		}
		refs, err := d.resolveImages(p.ThreadID, p.Images)
		if err != nil {
			return nil, err
		}
		cmd := contracts.Command{
			ID: d.nextID(), Type: contracts.CmdTurnStart, ThreadID: p.ThreadID, Text: text, Images: refs,
		}
		if p.Detach {
			return d.startDetachedTurn(ctx, p.ThreadID, prompt, cmd)
		}
		return d.runTurn(ctx, p.ThreadID, prompt, cmd)

	case "thread.describe":
		var p struct {
			ThreadID    contracts.ThreadID `json:"threadId"`
			Description string             `json:"description"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdThreadDescribe, ThreadID: p.ThreadID,
			Description: p.Description,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "issues.list":
		var p struct {
			ProjectID string `json:"projectId"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		// The board reads issues as an array on every status. A nil slice would
		// encode as null and crash the board, so every reply carries a slice.
		path, err := d.projectPath(p.ProjectID)
		if err != nil {
			return scm.IssueList{Status: scm.StatusNone, Message: err.Error(), Issues: []scm.Issue{}}, nil
		}
		list := scm.ListIssues(ctx, path)
		if list.Issues == nil {
			list.Issues = []scm.Issue{}
		}
		return list, nil

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
		if err := d.recordShell(ctx, p.ThreadID, command, res); err != nil {
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
		stopReports := d.startStatusReports(turnCtx, p.ThreadID)
		defer stopReports()
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
		if err := d.deleteThread(ctx, p.ThreadID, p.RemoveWorktree); err != nil {
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
		// Images must resolve before the steering message is recorded: a
		// refused ref leaves no transcript entry.
		refs, err := d.resolveImages(p.ThreadID, p.Images)
		if err != nil {
			return nil, err
		}
		inputs, err := d.imageInputs(p.ThreadID, refs)
		if err != nil {
			return nil, err
		}
		if err := d.steerThread(ctx, p.ThreadID, p.Text, prompt, refs, inputs); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "thread.btw":
		// A side question is answered from a snapshot and recorded nowhere: it
		// takes no turn slot, so it works while a turn runs without steering it.
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Question string             `json:"question"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Question) == "" {
			return nil, errors.New("question is required")
		}
		if d.runner == nil {
			return nil, errors.New("side questions are unavailable")
		}
		answer, err := d.runner.Btw(ctx, p.ThreadID, p.Question)
		if err != nil {
			return nil, err
		}
		return map[string]any{"answer": answer}, nil

	case "master.send":
		// The home-screen orchestrator answers from a snapshot and may propose a
		// thread. Dispatch is fire-and-forget: the first turn runs for as long
		// as the work takes, so the reply cannot wait for it.
		var p struct {
			Text    string                       `json:"text"`
			History []orchestrator.MasterMessage `json:"history"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Text) == "" {
			return nil, errors.New("text is required")
		}
		if d.master == nil {
			return nil, errors.New("master chat is unavailable")
		}
		result, err := d.master.Send(ctx, p.Text, p.History)
		if err != nil {
			return nil, err
		}
		d.runMaster(ctx, &result)
		return result, nil

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

	case "thread.setArchived":
		var p setBoolParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if err := d.setArchived(ctx, p.ThreadID, p.Enabled); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil

	case "thread.setStatusPeriod":
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Seconds  *int               `json:"seconds"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if p.Seconds == nil {
			return nil, errors.New("seconds is required")
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdStatusPeriodSet, ThreadID: p.ThreadID, StatusPeriod: p.Seconds,
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

	case "thread.setRoleRuntime":
		var p setRoleRuntimeParams
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if p.Role == "" {
			return nil, errors.New("role is required")
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID: d.nextID(), Type: contracts.CmdRoleRuntimeSet, ThreadID: p.ThreadID,
			RoleRuntime: &contracts.RoleRuntimeSet{
				Role:    contracts.RoleID(p.Role),
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

	case "usage.limits":
		// refresh bypasses the provider cache; the UI sends it on a manual refresh.
		var p struct {
			Refresh bool `json:"refresh"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if d.limits == nil {
			return []contracts.HarnessLimits{}, nil
		}
		return d.limits.Get(ctx, p.Refresh), nil

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

	case "app.info":
		version := d.version
		if version == "" {
			version = "dev"
		}
		return map[string]any{"version": version}, nil

	case "stats.teams":
		// Same scope as stats.harnesses: no params is the environment.
		var p struct {
			ThreadID  contracts.ThreadID `json:"threadId"`
			ProjectID string             `json:"projectId"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.engine.TeamStats(ctx, domain.HarnessScope{ThreadID: p.ThreadID, ProjectID: p.ProjectID})

	case "stats.models":
		// Same scope as stats.harnesses: no params is the environment.
		var p struct {
			ThreadID  contracts.ThreadID `json:"threadId"`
			ProjectID string             `json:"projectId"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return d.engine.ModelStats(ctx, domain.HarnessScope{ThreadID: p.ThreadID, ProjectID: p.ProjectID})

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

	case "update.takeReleaseNotes":
		// Read-and-clear: null when nothing is pending for the running build.
		if d.updater == nil {
			return nil, nil
		}
		n, ok, err := d.updater.TakeReleaseNotes()
		if err != nil || !ok {
			return nil, err
		}
		return n, nil

	case "deps.check":
		// Probes the forge CLIs on PATH with `--version`; local and read-only,
		// so the headless daemon reports the same answer as the desktop app.
		return deps.Probe(ctx), nil

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

	case "pr.list":
		return d.prList(ctx, req)

	case "pr.states":
		// Takes thread ids, never directories: the daemon resolves each thread's
		// own worktree, so a client cannot point gh at an arbitrary path.
		var p struct {
			ThreadIDs []contracts.ThreadID `json:"threadIds"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return threadStates(p.ThreadIDs, func(id contracts.ThreadID) string {
			st, err := d.engine.State(ctx, id)
			if err != nil || st.Worktree == nil {
				return ""
			}
			return d.threadWorkdir(ctx, id)
		}, d.prBoard.Peek, d.dirtyBoard.Peek), nil

	case "pr.commit", "pr.push", "pr.pull", "pr.create", "pr.ready", "pr.merge":
		return d.prAction(ctx, req)

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

	case "markdown.read":
		// Takes only a threadId, never a workdir: the viewer reads the thread's
		// own worktree or project and nothing a client names.
		var p struct {
			ThreadID contracts.ThreadID `json:"threadId"`
			Path     string             `json:"path"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		return files.ReadMarkdown(d.threadWorkdir(ctx, p.ThreadID), p.Path)

	case "fs.list", "fs.read", "fs.write":
		return d.fileEdit(ctx, req)

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
		// Validate the whole patch before mutating anything.
		if p.RoleEnabled != nil && !p.RoleEnabled.Enabled {
			if role, ok := d.roles.Get(contracts.RoleID(p.RoleEnabled.ID)); ok && role.Required {
				return nil, fmt.Errorf("required role %q cannot be disabled", role.ID)
			}
		}
		if p.Role != nil && p.Role.Harness != "" && !contracts.Harness(p.Role.Harness).Valid() {
			return nil, fmt.Errorf("unknown harness %q", p.Role.Harness)
		}
		if p.Lead != nil && p.Lead.Harness != "" && !contracts.Harness(p.Lead.Harness).Valid() {
			return nil, fmt.Errorf("unknown harness %q", p.Lead.Harness)
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
		if p.Lead != nil {
			// The lead runs on the Orchestrator role's runtime, so pinning it
			// is the same persisted setting, not a second source of truth.
			d.settings.SetRoleConfig(contracts.RoleOrchestrator, settings.RoleConfig{
				Harness: contracts.Harness(p.Lead.Harness),
				Model:   strings.TrimSpace(p.Lead.Model),
				Variant: strings.TrimSpace(p.Lead.Variant),
			})
		}
		if p.Harness != nil {
			d.settings.SetHarnessEnabled(contracts.Harness(p.Harness.ID), p.Harness.Enabled)
		}
		if p.RoleEnabled != nil {
			d.settings.SetRoleEnabled(contracts.RoleID(p.RoleEnabled.ID), p.RoleEnabled.Enabled)
		}
		if p.ExperimentalRoles != nil {
			d.settings.SetExperimentalRoles(*p.ExperimentalRoles)
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

	case "remote.status":
		st := d.RemoteStatus()
		if isRelayedSession(ctx) {
			// A pairing QR is a secret: the phone that reaches this daemon
			// through the relay must never be handed another surface's link.
			for i := range st.Items {
				st.Items[i].QR = ""
			}
		}
		return st, nil

	case "remote.whatsapp.connect":
		if d.remote == nil {
			return nil, errors.New("WhatsApp is unavailable")
		}
		err := d.remote.Connect(ctx)
		// The bridge may finish connecting later, but a failure is already a
		// state change every client should see.
		d.BroadcastRemoteStatus()
		if err != nil {
			return nil, err
		}
		return d.RemoteStatus(), nil

	case "remote.whatsapp.disconnect":
		if d.remote == nil {
			return nil, errors.New("WhatsApp is unavailable")
		}
		err := d.remote.Disconnect()
		d.BroadcastRemoteStatus()
		if err != nil {
			return nil, err
		}
		return d.RemoteStatus(), nil

	case "remote.whatsapp.control":
		// Inbound control lets the linked account's own chat drive the
		// orchestrator, so it is a desktop-only setting: a relayed phone cannot
		// reach remote.whatsapp.* at all.
		if d.remote == nil {
			return nil, errors.New("WhatsApp is unavailable")
		}
		var p struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decode(req.Params, &p); err != nil {
			return nil, err
		}
		if p.Enabled != nil {
			if err := d.remote.SetControl(*p.Enabled); err != nil {
				return nil, err
			}
			d.BroadcastRemoteStatus()
		}
		return ControlState{Enabled: d.remote.Control()}, nil

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
		// The phone row is derived from the relay, so its state changed too.
		d.BroadcastRemoteStatus()
		return rc.Status(), nil

	case "relay.disable":
		rc := d.relayControl()
		if err := rc.Disable(); err != nil {
			return nil, err
		}
		d.BroadcastRemoteStatus()
		return rc.Status(), nil

	case "relay.pair.start":
		return d.relayControl().PairStart(ctx)

	case "relay.devices.list":
		return map[string]any{"devices": d.relayControl().Devices()}, nil

	case "relay.tunnel.start":
		st, err := d.relayControl().StartTunnel(ctx)
		if err != nil {
			return nil, err
		}
		d.BroadcastRemoteStatus()
		return st, nil

	case "relay.tunnel.stop":
		rc := d.relayControl()
		if err := rc.StopTunnel(); err != nil {
			return nil, err
		}
		d.BroadcastRemoteStatus()
		return rc.TunnelStatus(), nil

	case "relay.tunnel.status":
		return d.relayControl().TunnelStatus(), nil

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
func (noRelay) StartTunnel(context.Context) (TunnelStatus, error) {
	return TunnelStatus{}, errNoRelay
}
func (noRelay) StopTunnel() error          { return errNoRelay }
func (noRelay) TunnelStatus() TunnelStatus { return TunnelStatus{} }

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
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return d.settings.Workdir()
	}
	return d.stateWorkdir(st)
}

// threadRoot is threadWorkdir for callers that must not guess: an unknown
// thread is an error, never the daemon default. engine.State returns an empty
// state rather than failing for an id it has never seen, so Exists is the test.
func (d *Dispatcher) threadRoot(ctx context.Context, id contracts.ThreadID) (string, error) {
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return "", err
	}
	if !st.Exists {
		return "", fmt.Errorf("thread %q does not exist", id)
	}
	return d.stateWorkdir(st), nil
}

// stateWorkdir picks the checkout for a thread's state: its worktree when it
// declares one, otherwise its project's path, otherwise the daemon default.
func (d *Dispatcher) stateWorkdir(st *domain.ThreadState) string {
	if d.settings == nil {
		return ""
	}
	base := d.settings.Workdir()
	if st.ProjectID != "" {
		if project, ok := d.settings.Project(st.ProjectID); ok && project.Path != "" {
			base = project.Path
		}
	}
	return worktree.Resolve(st.Worktree, base)
}

// fileEdit serves the desktop file editor. The root is always the thread's own
// working directory: clients name a path inside it, never a directory. An
// unknown thread fails instead of falling back to the daemon default.
func (d *Dispatcher) fileEdit(ctx context.Context, req Request) (any, error) {
	var p struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Path     string             `json:"path"`
		Text     string             `json:"text"`
		Hash     string             `json:"hash"`
	}
	if err := decode(req.Params, &p); err != nil {
		return nil, err
	}
	root, err := d.threadRoot(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	switch req.Method {
	case "fs.list":
		return fileedit.List(root, p.Path)
	case "fs.read":
		return fileedit.Read(root, p.Path)
	default:
		return fileedit.Write(root, p.Path, p.Text, p.Hash)
	}
}

// prAction runs one git or forge write from the PR tab in the thread's worktree.
// It takes only a threadId, never a workdir: a write must not land in a
// directory the client chose. Agent tool calls do not reach it.
func (d *Dispatcher) prAction(ctx context.Context, req Request) (any, error) {
	var p struct {
		ThreadID contracts.ThreadID `json:"threadId"`
		Message  string             `json:"message"`
		All      bool               `json:"all"`
		Title    string             `json:"title"`
	}
	if err := decode(req.Params, &p); err != nil {
		return nil, err
	}
	if p.ThreadID == "" {
		return nil, errors.New("threadId is required")
	}
	wd := d.threadWorkdir(ctx, p.ThreadID)
	if wd == "" {
		return nil, errors.New("thread has no workdir")
	}
	c := scm.Live()
	var (
		msg string
		err error
	)
	switch req.Method {
	case "pr.commit":
		msg, err = c.Commit(ctx, wd, p.Message, p.All)
	case "pr.push":
		msg, err = c.Push(ctx, wd)
	case "pr.pull":
		msg, err = c.Pull(ctx, wd)
	case "pr.create":
		msg, err = c.CreateRequest(ctx, wd, p.Title)
	case "pr.ready":
		msg, err = c.MarkReady(ctx, wd)
	case "pr.merge":
		msg, err = c.Merge(ctx, wd)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"message": msg}, nil
}

// threadBusy reports whether a thread has a turn in flight or an agent that
// has not finished. Both thread.delete and the lead's thread-delete refuse it.
func (d *Dispatcher) threadBusy(id contracts.ThreadID, st *domain.ThreadState) bool {
	if d.threadRunning(id) {
		return true
	}
	for _, a := range st.Agents {
		if !a.Status.Terminal() {
			return true
		}
	}
	return false
}

// deleteThread tombstones a thread, and with removeWorktree also removes the
// thread's own worktree before the tombstone. The running check and the dirty
// check (worktreeRemove refuses a dirty tree) both run before anything is
// removed, so those refusals keep the worktree. The decider is the last gate and
// runs after the removal, so a turn or agent that starts between the running
// check and the dispatch can still get the delete refused, leaving the thread
// with its worktree already gone. The window is one engine round-trip. Closing it
// would need worktree removal inside the decider, which the engine does not do.
// setArchived archives or restores a thread. Archiving a busy thread is refused
// with the same message delete uses, so the user sees what to do. The decider
// still enforces the rule for races that land after this check.
func (d *Dispatcher) setArchived(ctx context.Context, id contracts.ThreadID, archived bool) error {
	if archived {
		st, err := d.engine.State(ctx, id)
		if err != nil {
			return err
		}
		if st.Exists && d.threadBusy(id, st) {
			return fmt.Errorf("%w: thread %q is running; stop it first", domain.ErrInvalidCommand, id)
		}
	}
	return d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdArchiveSet, ThreadID: id, Archived: &archived,
	})
}

func (d *Dispatcher) deleteThread(ctx context.Context, id contracts.ThreadID, removeWorktree bool) error {
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return err
	}
	if !st.Exists {
		return fmt.Errorf("thread %q does not exist: %w", id, domain.ErrThreadNotFound)
	}
	if d.threadBusy(id, st) {
		return fmt.Errorf("%w: thread %q is running; stop it first", domain.ErrInvalidCommand, id)
	}
	if removeWorktree && st.Worktree != nil && !st.Worktree.Inherited {
		if _, err := d.worktreeRemove(ctx, st.ProjectID, st.Worktree.Path, false); err != nil {
			return err
		}
	}
	return d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdThreadDelete, ThreadID: id,
	})
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

// forkThread starts a new top-level thread from the source's transcript up to
// and including messageID. Message ids are the stable key: they are unique per
// thread and survive the copy. The source is only read. The fork has no
// worktree yet; its first turn cuts one from the project base like any new
// thread. Provider sessions are per turn, so the copied messages reach the
// agent through the transcript prompt section, not a resume id.
func (d *Dispatcher) forkThread(ctx context.Context, p forkThreadParams) (any, error) {
	if p.MessageID == "" {
		return nil, errors.New("messageId is required")
	}
	src, err := d.engine.State(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if !src.Exists {
		return nil, fmt.Errorf("thread %q does not exist", p.ThreadID)
	}
	end := -1
	for i, m := range src.Messages {
		if m.ID == p.MessageID {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("message %q is not in thread %q", p.MessageID, p.ThreadID)
	}
	transcript := make([]contracts.MessageSent, 0, end+1)
	for _, m := range src.Messages[:end+1] {
		transcript = append(transcript, contracts.MessageSent{MessageID: m.ID, Author: m.Author, Text: m.Text})
	}
	id := d.newThreadID()
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID: d.nextID(), Type: contracts.CmdThreadFork, ThreadID: id,
		Title: src.Title + " (branch)", ProjectID: src.ProjectID, Transcript: transcript,
	}); err != nil {
		return nil, err
	}
	d.auditLog("thread.forked", map[string]any{
		"threadId": string(id), "from": string(p.ThreadID), "messages": len(transcript),
	})
	return map[string]any{"threadId": id}, nil
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
	// Enabled is the effective usability: required roles are always enabled.
	Enabled      bool `json:"enabled"`
	Required     bool `json:"required"`
	Experimental bool `json:"experimental"`
}

// LeadRuntimeView is the master orchestrator's configured runtime override.
// Empty fields inherit the Orchestrator role's defaults, and a thread's
// per-lead override still wins over it.
type LeadRuntimeView struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Variant string `json:"variant"`
}

// SettingsView is the settings screen's payload.
type SettingsView struct {
	Workdir           string           `json:"workdir"`
	AutoMode          bool             `json:"autoMode"`
	ApprovedModels    []string         `json:"approvedModels"`
	Roles             []RoleConfigView `json:"roles"`
	Lead              LeadRuntimeView  `json:"lead"`
	ConfigPath        string           `json:"configPath"`
	StaleAfterMinutes int              `json:"staleAfterMinutes"`
	ExperimentalRoles bool             `json:"experimentalRoles"`
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
	// Lead pins the master orchestrator's runtime; it is stored as the
	// Orchestrator role's runtime, so unset fields keep inheriting it.
	Lead    *LeadRuntimeView `json:"lead"`
	Harness *struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	} `json:"harness"`
	RoleEnabled *struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	} `json:"roleEnabled"`
	ExperimentalRoles *bool `json:"experimentalRoles"`
}

func (d *Dispatcher) settingsView() SettingsView {
	var view SettingsView
	if d.settings != nil {
		view.Workdir = d.settings.Workdir()
		view.AutoMode = d.settings.AutoMode()
		view.ConfigPath = d.settings.Path()
		view.StaleAfterMinutes = d.settings.StaleAfterMinutes()
		view.ExperimentalRoles = d.settings.ExperimentalRoles()
		for m := range d.settings.ApprovedModels() {
			view.ApprovedModels = append(view.ApprovedModels, m)
		}
		sort.Strings(view.ApprovedModels)
		if cfg, ok := d.settings.RoleConfig(contracts.RoleOrchestrator); ok {
			view.Lead.Model = cfg.Model
			view.Lead.Variant = cfg.Variant
			if cfg.Harness.Valid() {
				view.Lead.Harness = string(cfg.Harness)
			}
		}
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
		enabled := true
		if d.settings != nil {
			enabled = role.Required || d.settings.RoleEnabled(role.ID)
		}
		view.Roles = append(view.Roles, RoleConfigView{
			ID:           string(role.ID),
			Name:         role.Name,
			Harness:      string(harness),
			Model:        model,
			Variant:      variant,
			Builtin:      role.Builtin,
			Enabled:      enabled,
			Required:     role.Required,
			Experimental: role.Experimental,
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
	contracts.EvtSteeringInjected:   true,
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
