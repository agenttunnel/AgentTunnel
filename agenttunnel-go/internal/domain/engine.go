package domain

import (
	"context"
	"errors"
	"fmt"
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

	mu     sync.RWMutex
	states map[contracts.ThreadID]*ThreadState
	subs   map[contracts.ThreadID]map[int]chan contracts.Event
	nextID int

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

// Roles exposes the engine's role registry.
func (e *Engine) Roles() *contracts.RoleRegistry { return e.roles }

// Start begins processing commands. It returns immediately; cancel ctx or call
// Close to stop.
func (e *Engine) Start(ctx context.Context) {
	go e.loop(ctx)
}

func (e *Engine) loop(ctx context.Context) {
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
	payloads, err := Decide(cmd, state)
	if err != nil {
		e.auditLog("command.rejected", map[string]any{
			"type": string(cmd.Type), "threadId": string(cmd.ThreadID), "error": err.Error(),
		})
		return err
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
	listeners := make([]chan contracts.Event, 0, len(e.subs[cmd.ThreadID]))
	for _, ch := range e.subs[cmd.ThreadID] {
		listeners = append(listeners, ch)
	}
	e.mu.Unlock()

	for _, ev := range events {
		e.auditLog("event", map[string]any{
			"type":     string(ev.Type()),
			"threadId": string(ev.ThreadID),
			"seq":      ev.Seq,
		})
		for _, ch := range listeners {
			select {
			case ch <- ev:
			default: // slow subscriber: drop rather than block the worker
			}
		}
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

// Threads lists every thread with a durable event log, minus tombstoned ones:
// loading each state is O(n) once and cached thereafter.
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
	return out, nil
}

// Publish fans an event out to a thread's subscribers without persisting it. Use
// it for high-volume, ephemeral progress (agent output) that must not enter the
// event log or the read model.
func (e *Engine) Publish(threadID contracts.ThreadID, payload contracts.EventPayload) {
	ev := contracts.Event{ThreadID: threadID, At: e.clock(), Payload: payload}

	e.mu.RLock()
	listeners := make([]chan contracts.Event, 0, len(e.subs[threadID]))
	for _, ch := range e.subs[threadID] {
		listeners = append(listeners, ch)
	}
	e.mu.RUnlock()

	for _, ch := range listeners {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block the producer
		}
	}
}

// Subscribe returns a channel of committed events for a thread, plus an
// unsubscribe function. The channel is closed when unsubscribed.
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
			if c, ok := m[key]; ok {
				delete(m, key)
				close(c)
			}
		}
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
