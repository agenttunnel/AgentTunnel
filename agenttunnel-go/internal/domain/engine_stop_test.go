package domain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A command sent to a stopped engine must not block its caller. The command
// channel is buffered, so the send alone succeeds, and a loop that is still
// winding down may or may not take it; either way the wait for the result must
// end, or a caller such as a turn's release blocks forever.
func TestDispatchToStoppedEngineReturns(t *testing.T) {
	stoppers := map[string]func(e *Engine, cancel context.CancelFunc){
		"closed":    func(e *Engine, _ context.CancelFunc) { e.Close() },
		"cancelled": func(_ *Engine, cancel context.CancelFunc) { cancel() },
	}
	for name, stop := range stoppers {
		e := NewEngine(NewMemoryStore(), nil)
		ctx, cancel := context.WithCancel(context.Background())
		e.Start(ctx)
		stop(e, cancel)

		errc := make(chan error, 1)
		go func() {
			errc <- e.Dispatch(context.Background(), contracts.Command{ID: "c", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
		}()
		select {
		case <-errc:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: dispatch to a stopped engine never returned", name)
		}
		cancel()
	}
}

// gatedStore holds every append until released, so a test can hold the engine's
// worker mid-write the way a slow disk would.
type gatedStore struct {
	EventStore
	started chan struct{}
	release chan struct{}
}

func (s *gatedStore) Append(ctx context.Context, threadID contracts.ThreadID, payloads []contracts.EventPayload, clock Clock) ([]contracts.Event, error) {
	select {
	case s.started <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	<-s.release
	return s.EventStore.Append(ctx, threadID, payloads, clock)
}

// Close must join the worker. Background writers — a subthread's title
// generation, for one — dispatch after the turn that spawned them is over, so
// the only point at which a caller can know the event log is quiescent is
// after Close returns. A Close that returned while the worker was still
// appending raced every later state-directory cleanup after it (the subthread
// TempDir CI flake: a recreated threads/<id>.jsonl failing RemoveAll).
func TestCloseWaitsForInFlightAppend(t *testing.T) {
	store := &gatedStore{
		EventStore: NewMemoryStore(),
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	e := NewEngine(store, nil)
	e.Start(context.Background())

	dispatched := make(chan error, 1)
	go func() {
		dispatched <- e.Dispatch(context.Background(), contracts.Command{
			ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Draft: true,
		})
	}()
	<-store.started // the worker is inside Append

	closed := make(chan struct{})
	go func() {
		e.Close()
		close(closed)
	}()

	// The worker is mid-append, so Close must not have returned: a state-dir
	// removal now would race the write it is still committing.
	select {
	case <-closed:
		t.Fatal("Close returned while the worker was still appending")
	default:
	}

	close(store.release)
	<-closed // the append finished, so Close may return now
	// The waiter returns either its result or ErrEngineClosed — a command
	// already in flight when Close lands can report either — but the write
	// itself must have been committed before Close returned.
	if err := <-dispatched; err != nil && !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("dispatch: %v", err)
	}
	st, err := e.State(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists {
		t.Fatal("Close returned before the in-flight append was committed")
	}
}

// Once Close has returned, a late dispatch from any background writer must
// append nothing: the worker has exited, so the dispatch fails instead.
func TestDispatchAfterCloseAppendsNothing(t *testing.T) {
	e := NewEngine(NewMemoryStore(), nil)
	e.Start(context.Background())
	e.Close()

	if err := e.Dispatch(context.Background(), contracts.Command{
		ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Draft: true,
	}); err == nil {
		t.Fatal("dispatch after Close succeeded; want engine closed")
	}
	st, err := e.State(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Exists {
		t.Fatalf("thread materialized after Close; state = %+v", st)
	}
}
