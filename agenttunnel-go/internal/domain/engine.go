package domain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/actionlog"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Engine is the orchestration core. Clients dispatch typed commands; a single
// worker goroutine drains them in order, the decider turns each into events, the
// store appends them, and the projector folds them into the read model. This
// mirrors AgentTunnel's totally-ordered command/event/projection loop.
type Engine struct {
	store EventStore
	roles *contracts.RoleRegistry
	clock Clock

	mu      sync.RWMutex
	states  map[contracts.ThreadID]*ThreadState
	subs    map[contracts.ThreadID]map[int]chan contracts.Event
	allSubs map[int]chan contracts.Event
	nextID  int

	cmds chan envelope
	done chan struct{}
	once sync.Once

	audit actionlog.Func
}

// SetAudit installs a hook that records every command and committed event. Set
// it before Start; the worker calls it inline, so it must be cheap.
func (e *Engine) SetAudit(f actionlog.Func) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.audit = f
}

func (e *Engine) auditLog(kind string, fields map[string]any) {
	if e.audit != nil {
		e.audit(kind, fields)
	}
}

type envelope struct {
	cmd contracts.Command
	res chan error
}

// NewEngine wires a store and role registry into an engine.
func NewEngine(store EventStore, roles *contracts.RoleRegistry) *Engine {
	if roles == nil {
		roles = contracts.NewRoleRegistry(contracts.BuiltinRoles()...)
	}
	return &Engine{
		store:  store,
		roles:  roles,
		clock:  time.Now,
		states: make(map[contracts.ThreadID]*ThreadState),
		subs:   make(map[contracts.ThreadID]map[int]chan contracts.Event),
		cmds:   make(chan envelope, 256),
		done:   make(chan struct{}),
	}
}

// SetClock overrides the engine clock (used by tests).
func (e *Engine) SetClock(c Clock) { e.clock = c }

// Now reads the engine clock, so derived views (like thread staleness) agree
// with event timestamps even in tests.
func (e *Engine) Now() time.Time { return e.clock() }

// Roles exposes the engine's role registry.
func (e *Engine) Roles() *contracts.RoleRegistry { return e.roles }

// Start begins processing commands. It returns immediately; cancel ctx or call
// Close to stop.
func (e *Engine) Start(ctx context.Context) {
	go e.loop(ctx)
}

func (e *Engine) loop(ctx context.Context) {
	// However the loop ends, the engine is done: waiting callers must not block
	// on a loop that no longer runs.
	defer e.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.done:
			return
		case env := <-e.cmds:
			env.res <- e.process(ctx, env.cmd)
		}
	}
}

// Close stops the engine.
func (e *Engine) Close() { e.once.Do(func() { close(e.done) }) }

// Dispatch enqueues a command and blocks until it is processed, returning any
// decision/append error. Ordering across concurrent callers is total.
func (e *Engine) Dispatch(ctx context.Context, cmd contracts.Command) error {
	res := make(chan error, 1)
	select {
	case e.cmds <- envelope{cmd: cmd, res: res}:
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		return errors.New("engine closed")
	}
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		// The loop exited after taking the command, or never will take it: the
		// result is returned if it already landed, otherwise the engine is gone.
		select {
		case err := <-res:
			return err
		default:
			return errors.New("engine closed")
		}
	}
}

func (e *Engine) process(ctx context.Context, cmd contracts.Command) error {
	e.auditLog("command", map[string]any{
		"id":       cmd.ID,
		"type":     string(cmd.Type),
		"threadId": string(cmd.ThreadID),
	})

	state, err := e.stateFor(ctx, cmd.ThreadID)
	if err != nil {
		return err
	}

	// Thread creation is in-memory only: a thread that is never used leaves no
	// trace on disk. The creation events are written by the first durable
	// command instead (materialize below). A create for an id that already
	// exists — durable or provisional — still falls through to the decider,
	// which rejects it.
	if cmd.Type == contracts.CmdThreadCreate && !state.Exists {
		e.mu.Lock()
		state.Exists = true
		state.Provisional = true
		state.PeerMailbox = true
		state.Title = strings.TrimSpace(cmd.Title)
		if state.Title == "" {
			state.Title = contracts.DefaultThreadTitle
		}
		state.ProjectID = cmd.ProjectID
		state.ParentThreadID = cmd.ParentThreadID
		state.Worktree = cmd.Worktree
		state.UpdatedAt = e.clock()
		e.states[cmd.ThreadID] = state
		e.mu.Unlock()
		return nil
	}

	// Deleting a provisional thread just forgets it: there is nothing on disk
	// to tombstone, and writing creation-plus-deletion events would persist
	// the very thread the caller is discarding.
	if state.Provisional && cmd.Type == contracts.CmdThreadDelete {
		e.mu.Lock()
		delete(e.states, cmd.ThreadID)
		e.mu.Unlock()
		return nil
	}

	// The first durable command materializes the thread: its creation events
	// are appended and projected first, so the log reads as if the thread had
	// always existed.
	if state.Provisional {
		if err := e.materialize(ctx, cmd.ThreadID, state); err != nil {
			return err
		}
	}

	payloads, err := Decide(cmd, state)
	if err != nil {
		e.auditLog("command.rejected", map[string]any{
			"type": string(cmd.Type), "threadId": string(cmd.ThreadID), "error": err.Error(),
		})
		return err
	}
	if len(payloads) == 0 {
		return nil
	}
	events, err := e.store.Append(ctx, cmd.ThreadID, payloads, e.clock)
	if err != nil {
		return err
	}

	e.mu.Lock()
	for _, ev := range events {
		if err := Project(state, ev); err != nil {
			e.mu.Unlock()
			return err
		}
	}
	listeners := e.listenersLocked(cmd.ThreadID)
	e.mu.Unlock()

	for _, ev := range events {
		e.auditLog("event", map[string]any{
			"type":     string(ev.Type()),
			"threadId": string(ev.ThreadID),
			"seq":      ev.Seq,
		})
		deliver(listeners, ev)
	}
	return nil
}

// materialize writes a provisional thread's creation events — the same payload
// the decider returns for thread.create — so the thread becomes durable the
// moment something is actually done with it. Mirrors the append/project/notify
// tail of process.
func (e *Engine) materialize(ctx context.Context, id contracts.ThreadID, state *ThreadState) error {
	payloads := []contracts.EventPayload{
		contracts.ThreadCreated{Title: state.Title, ProjectID: state.ProjectID, ParentThreadID: state.ParentThreadID, Worktree: state.Worktree},
		contracts.PeerMailboxSet{Enabled: true},
	}
	events, err := e.store.Append(ctx, id, payloads, e.clock)
	if err != nil {
		return err
	}
	e.mu.Lock()
	for _, ev := range events {
		if err := Project(state, ev); err != nil {
			e.mu.Unlock()
			return err
		}
	}
	state.Provisional = false
	listeners := e.listenersLocked(id)
	e.mu.Unlock()
	for _, ev := range events {
		deliver(listeners, ev)
	}
	return nil
}

func (e *Engine) stateFor(ctx context.Context, id contracts.ThreadID) (*ThreadState, error) {
	e.mu.RLock()
	if s, ok := e.states[id]; ok {
		e.mu.RUnlock()
		return s, nil
	}
	e.mu.RUnlock()

	events, err := e.store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	s := NewThreadState(id)
	for _, ev := range events {
		if err := Project(s, ev); err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	e.states[id] = s
	e.mu.Unlock()
	return s, nil
}

// Events returns a thread's durable event log, oldest first. It reads the
// store directly and never touches the cached projection, so it is safe to
// call while turns are running.
func (e *Engine) Events(ctx context.Context, id contracts.ThreadID) ([]contracts.Event, error) {
	return e.store.Load(ctx, id)
}

// State returns a consistent snapshot of a thread's read model.
func (e *Engine) State(ctx context.Context, id contracts.ThreadID) (*ThreadState, error) {
	s, err := e.stateFor(ctx, id)
	if err != nil {
		return nil, err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return s.Clone(), nil
}

// Threads lists every thread with a durable event log, plus provisional ones
// still open in memory, minus tombstoned ones: loading each state is O(n) once
// and cached thereafter.
func (e *Engine) Threads(ctx context.Context) ([]contracts.ThreadID, error) {
	ids, err := e.store.Threads(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]contracts.ThreadID, 0, len(ids))
	for _, id := range ids {
		s, err := e.stateFor(ctx, id)
		if err != nil {
			return nil, err
		}
		if !s.Deleted {
			out = append(out, id)
		}
	}
	// Provisional threads exist only in memory; they are listed (the UI has
	// them open) but live in no event log yet.
	e.mu.RLock()
	pending := make([]contracts.ThreadID, 0, 4)
	for id, s := range e.states {
		if s.Provisional && !s.Deleted {
			pending = append(pending, id)
		}
	}
	e.mu.RUnlock()
	return append(out, pending...), nil
}

// Publish fans an event out to a thread's subscribers without persisting it. Use
// it for high-volume, ephemeral progress (agent output) that must not enter the
// event log or the read model.
func (e *Engine) Publish(threadID contracts.ThreadID, payload contracts.EventPayload) {
	ev := contracts.Event{ThreadID: threadID, At: e.clock(), Payload: payload}

	e.mu.RLock()
	listeners := e.listenersLocked(threadID)
	e.mu.RUnlock()

	deliver(listeners, ev)
}

// listenersLocked snapshots every subscriber that should hear an event for
// threadID: the thread's own subscribers and the engine-wide ones. Callers hold
// e.mu.
func (e *Engine) listenersLocked(threadID contracts.ThreadID) []chan contracts.Event {
	out := make([]chan contracts.Event, 0, len(e.subs[threadID])+len(e.allSubs))
	for _, ch := range e.subs[threadID] {
		out = append(out, ch)
	}
	for _, ch := range e.allSubs {
		out = append(out, ch)
	}
	return out
}

// deliver queues ev to every listener, dropping it for a listener that is not
// keeping up rather than blocking the engine.
func deliver(listeners []chan contracts.Event, ev contracts.Event) {
	for _, ch := range listeners {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block the worker
		}
	}
}

// Subscribe returns a channel of committed events for a thread, plus an
// unsubscribe function. Unsubscribing removes the channel from the engine; it
// is NOT closed — senders snapshot the listener set under the engine lock and
// deliver after releasing it, so closing here could panic a concurrent send.
// Consumers stop via their own context, the way the transport does; the
// channel and any undelivered events are garbage once unsubscribed.
func (e *Engine) Subscribe(id contracts.ThreadID) (<-chan contracts.Event, func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.subs[id] == nil {
		e.subs[id] = make(map[int]chan contracts.Event)
	}
	key := e.nextID
	e.nextID++
	ch := make(chan contracts.Event, 256)
	e.subs[id][key] = ch
	return ch, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if m := e.subs[id]; m != nil {
			delete(m, key)
		}
	}
}

// SubscribeAll returns a channel of committed events from every thread, plus an
// unsubscribe function. Use it for daemon-wide observers (like the remote
// bridge) that do not know which threads exist yet. The channel is never
// closed, for the same reason Subscribe's is not.
func (e *Engine) SubscribeAll() (<-chan contracts.Event, func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.allSubs == nil {
		e.allSubs = make(map[int]chan contracts.Event)
	}
	key := e.nextID
	e.nextID++
	ch := make(chan contracts.Event, 256)
	e.allSubs[key] = ch
	return ch, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.allSubs, key)
	}
}

// NewCommand constructs a command with a fresh id.
func NewCommand(typ contracts.CommandType, threadID contracts.ThreadID) contracts.Command {
	return contracts.Command{
		ID:       fmt.Sprintf("cmd-%d", time.Now().UnixNano()),
		Type:     typ,
		ThreadID: threadID,
	}
}
