package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// AuthorScheduler marks the daemon-written message that carries a triggered
// schedule's compiled results. It is never "user": a user-authored message
// opens a turn in the projector, and the scheduler's note must join the turn
// the trigger itself opened rather than inflate the turn count.
const AuthorScheduler = "scheduler"

// The thread scheduler is the runtime half of thread scheduling. The decider
// sees one thread at a time, so everything that spans threads lives here:
//
//   - arming a schedule checks that every dependency exists and that no
//     dependency waits (directly or through a chain) on the thread being
//     armed, which would deadlock the schedule;
//   - once every dependency of an armed thread has finished, the schedule
//     fires: the thread's turn starts through the normal turn path, carrying
//     the scheduled prompt plus a compiled RESULTS section from its
//     dependencies.
//
// Triggers are driven by engine events, so they fire for schedules armed any
// way — the RPC, a tool, a raw engine dispatch. StartScheduler also recovers
// at boot: a crash between a dependency finishing and the trigger leaves an
// armed thread whose dependencies are all done, and that turn starts as soon
// as the daemon is back.

// StartScheduler subscribes the trigger loop engine-wide and runs the boot
// recovery scan. The subscription is live before it returns, so no event can
// sneak past it; the daemon assembly calls this before its server exists. The
// returned stop function ends the loop and joins it.
func (d *Dispatcher) StartScheduler(ctx context.Context) func() {
	events, unsub := d.engine.SubscribeAll()
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-loopCtx.Done():
				return
			case ev := <-events:
				d.onScheduleEvent(loopCtx, ev)
			}
		}
	}()
	// Schedules whose dependencies finished while the daemon was down fire
	// now, through the same path a live event would take.
	d.recoverSchedules(loopCtx)
	return func() {
		cancel()
		unsub()
		<-done
	}
}

// recoverSchedules rebuilds the armed-thread index from the durable state and
// evaluates every waiting thread once.
func (d *Dispatcher) recoverSchedules(ctx context.Context) {
	ids, err := d.engine.Threads(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		st, err := d.engine.State(ctx, id)
		if err != nil || st.Waiting == nil {
			continue
		}
		d.scheduleArm(ctx, id)
	}
}

// onScheduleEvent keeps the armed-thread index current and re-evaluates
// waiting threads whenever a dependency may have advanced. Lookups happen
// after the type switch, and the loop itself stays trivial, so high-volume
// payloads (agent output) cost one comparison.
func (d *Dispatcher) onScheduleEvent(ctx context.Context, ev contracts.Event) {
	switch p := ev.Payload.(type) {
	case contracts.ThreadScheduled:
		d.scheduleArm(ctx, ev.ThreadID)
	case contracts.ThreadTriggered, contracts.ThreadScheduleCancelled:
		d.scheduleForget(ev.ThreadID)
	case contracts.ThreadDeleted:
		// A deleted thread is neither a waiter nor an unfinished dependency.
		d.scheduleForget(ev.ThreadID)
		d.scheduleDepAdvanced(ctx, ev.ThreadID)
		// Nor can it keep controlling the threads it relaunched: its claim
		// would misattribute every later user- or scheduler-started turn on
		// them to a controller that no longer exists.
		d.releaseControlledThreads(ctx, ev.ThreadID)
	case contracts.AgentStatusChanged:
		if p.Status.Terminal() {
			d.scheduleDepAdvanced(ctx, ev.ThreadID)
			// A thread whose own trigger attempt failed — its turn slot was
			// taken when the fire arrived, or its worktree could not be
			// restored — stays armed. Its own agents settling is a new
			// chance to fire; the state re-read inside drops threads that
			// are not waiting, so the extra lookup is all this costs.
			d.scheduleEvaluate(ctx, ev.ThreadID)
		}
	}
}

// armSchedule is the thread.schedule RPC: it validates what the decider
// cannot (cross-thread facts), arms the schedule, and lets the trigger logic
// look at the newly waiting thread — a schedule on dependencies that are all
// finished fires right away.
func (d *Dispatcher) armSchedule(ctx context.Context, threadID contracts.ThreadID, deps []contracts.ThreadID, prompt string) error {
	st, err := d.engine.State(ctx, threadID)
	if err != nil {
		return err
	}
	if !st.Exists {
		return fmt.Errorf("thread %q does not exist", threadID)
	}
	for _, dep := range deps {
		depSt, err := d.engine.State(ctx, dep)
		if err != nil {
			return err
		}
		if !depSt.Exists {
			return fmt.Errorf("unknown dependency thread %q", dep)
		}
	}
	if err := d.checkScheduleAcyclic(ctx, threadID, deps); err != nil {
		return err
	}
	if err := d.engine.Dispatch(ctx, contracts.Command{
		ID:       d.nextID(),
		Type:     contracts.CmdThreadSchedule,
		ThreadID: threadID,
		Schedule: &contracts.ScheduleSpec{DependsOn: deps, Prompt: prompt},
	}); err != nil {
		return err
	}
	d.scheduleArm(ctx, threadID)
	return nil
}

// checkScheduleAcyclic refuses a schedule whose dependency graph leads back to
// the thread being armed: that thread would wait on itself through a chain of
// waiting threads and could never fire. The walk carries a seen set on both
// the queue and the pattern itself, so re-entrant chains are visited once.
func (d *Dispatcher) checkScheduleAcyclic(ctx context.Context, threadID contracts.ThreadID, deps []contracts.ThreadID) error {
	seen := make(map[contracts.ThreadID]bool, len(deps))
	queue := make([]contracts.ThreadID, 0, len(deps))
	for _, dep := range deps {
		if !seen[dep] {
			seen[dep] = true
			queue = append(queue, dep)
		}
	}
	for len(queue) > 0 {
		cur := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		st, err := d.engine.State(ctx, cur)
		if err != nil {
			return err
		}
		if st.Waiting == nil {
			continue
		}
		for _, next := range st.Waiting.DependsOn {
			if next == threadID {
				return fmt.Errorf("thread %s waits on %s (directly or through a chain), so scheduling %s on it would never fire", cur, threadID, threadID)
			}
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return nil
}

// releaseControlledThreads clears the given thread's claim on every thread it
// controls — the engine-wide cleanup that must follow a controller's
// deletion, like the armed index maintenance above. Relaunch permission is
// not lost by the threads that were controlled: the controller drove them
// because they fired one of its schedules, and that TriggeredFrom record is
// never cleared.
func (d *Dispatcher) releaseControlledThreads(ctx context.Context, controller contracts.ThreadID) {
	ids, err := d.engine.Threads(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		if id == controller {
			continue
		}
		st, err := d.engine.State(ctx, id)
		if err != nil || st.ControlledBy != controller {
			continue
		}
		if err := d.engine.Dispatch(ctx, contracts.Command{
			ID:         d.nextID(),
			Type:       contracts.CmdThreadControl,
			ThreadID:   id,
			Controller: "",
		}); err != nil {
			// A refusal here is a benign race: the release was already run by
			// the thread.delete path or the delete event, which both own this
			// cleanup.
			if !errors.Is(err, domain.ErrInvalidCommand) {
				d.auditLog("thread.control.release.failed", map[string]any{
					"threadId": string(id), "controller": string(controller), "error": err.Error(),
				})
			}
		}
	}
}

// scheduleArm re-reads a thread's waiting spec and evaluates it. Called when a
// thread is scheduled — by RPC, tool, or engine event — and during recovery.
func (d *Dispatcher) scheduleArm(ctx context.Context, id contracts.ThreadID) {
	st, err := d.engine.State(ctx, id)
	if err != nil {
		return
	}
	d.schedMu.Lock()
	if st.Waiting != nil {
		if d.schedArmed == nil {
			d.schedArmed = map[contracts.ThreadID]map[contracts.ThreadID]bool{}
		}
		deps := make(map[contracts.ThreadID]bool, len(st.Waiting.DependsOn))
		for _, dep := range st.Waiting.DependsOn {
			deps[dep] = true
		}
		d.schedArmed[id] = deps
	} else {
		delete(d.schedArmed, id)
	}
	d.schedMu.Unlock()
	d.scheduleEvaluate(ctx, id)
}

// scheduleForget drops a thread from the armed index: it fired or was
// disarmed.
func (d *Dispatcher) scheduleForget(id contracts.ThreadID) {
	d.schedMu.Lock()
	delete(d.schedArmed, id)
	d.schedMu.Unlock()
}

// scheduleDepAdvanced re-evaluates every waiting thread that lists dep as a
// dependency.
func (d *Dispatcher) scheduleDepAdvanced(ctx context.Context, dep contracts.ThreadID) {
	d.schedMu.Lock()
	var affected []contracts.ThreadID
	for id, deps := range d.schedArmed {
		if deps[dep] {
			affected = append(affected, id)
		}
	}
	d.schedMu.Unlock()
	for _, id := range affected {
		d.scheduleEvaluate(ctx, id)
	}
}

// scheduleEvaluate fires a waiting thread when every one of its dependencies
// has finished. The state is the source of truth: a stale armed index entry
// is dropped here, and a single-flight guard keeps concurrent evaluations
// (the RPC kick racing a dependency's terminal event) from starting two turns.
func (d *Dispatcher) scheduleEvaluate(ctx context.Context, id contracts.ThreadID) {
	d.schedMu.Lock()
	if d.schedFiring[id] {
		d.schedMu.Unlock()
		return
	}
	st, err := d.engine.State(ctx, id)
	if err != nil {
		d.schedMu.Unlock()
		return
	}
	if st.Waiting == nil {
		delete(d.schedArmed, id)
		d.schedMu.Unlock()
		return
	}
	ready := true
	for _, dep := range st.Waiting.DependsOn {
		depSt, err := d.engine.State(ctx, dep)
		if err != nil || !scheduleDepFinished(depSt) {
			ready = false
			break
		}
	}
	if !ready {
		d.schedMu.Unlock()
		return
	}
	if d.schedFiring == nil {
		d.schedFiring = map[contracts.ThreadID]bool{}
	}
	d.schedFiring[id] = true
	d.schedMu.Unlock()

	d.auditLog("schedule.ready", map[string]any{
		"threadId": string(id), "deps": st.Waiting.DependsOn,
	})
	go d.fireSchedule(id)
}

// scheduleFireDone releases the single-flight claim once the trigger turn has
// been claimed (or refused), so a thread re-armed later is not shadowed by a
// claim from an earlier fire.
func (d *Dispatcher) scheduleFireDone(id contracts.ThreadID) {
	d.schedMu.Lock()
	delete(d.schedFiring, id)
	d.schedMu.Unlock()
}

// fireSchedule runs a waiting thread's turn. It walks the same path
// thread.turn.start does — turn slot, worktree gate, status reports — but the
// transcript is written by two authors: the user's own scheduled prompt opens
// the turn (which keeps per-turn spend accounting on the turn the trigger
// actually runs), and the compiled RESULTS section is appended as a scheduler
// note that joins that turn without opening another. The turn text is built
// once and carries both; it is the opener AND the harness prompt, so
// priorMessages — which finds the turn boundary by matching the current
// message — cannot mistake the opener for earlier conversation and hand the
// lead its own prompt and the results twice.
func (d *Dispatcher) fireSchedule(id contracts.ThreadID) {
	base := d.baseCtx
	if base == nil {
		base = context.Background()
	}
	turnCtx, done, err := d.beginTurn(base, id)
	if err != nil {
		d.scheduleFireDone(id)
		d.auditLog("schedule.trigger.failed", map[string]any{"threadId": string(id), "error": err.Error()})
		return
	}
	defer done()
	if err := d.ensureWorktree(base, id); err != nil {
		d.scheduleFireDone(id)
		d.auditLog("schedule.trigger.failed", map[string]any{"threadId": string(id), "error": err.Error()})
		return
	}
	st, err := d.engine.State(base, id)
	if err != nil || st.Waiting == nil {
		// Disarmed while the claim was in flight; the cancel event already
		// dropped it from the index.
		d.scheduleFireDone(id)
		return
	}
	results := d.scheduleResults(base, st.Waiting.DependsOn)
	prompt := st.Waiting.Prompt + "\n\n" + results
	if err := d.engine.Dispatch(base, contracts.Command{
		ID:       d.nextID(),
		Type:     contracts.CmdTurnStart,
		ThreadID: id,
		Text:     prompt,
	}); err != nil {
		d.scheduleFireDone(id)
		d.auditLog("schedule.trigger.failed", map[string]any{"threadId": string(id), "error": err.Error()})
		return
	}
	d.scheduleFireDone(id)
	d.auditLog("schedule.triggered", map[string]any{
		"threadId": string(id), "deps": st.Waiting.DependsOn,
	})

	// The results note joins the turn the trigger opened; a failure to record
	// it must not cancel work that was already promised.
	if err := d.engine.Dispatch(base, contracts.Command{
		ID:       d.nextID(),
		Type:     contracts.CmdMessageAppend,
		ThreadID: id,
		Message:  &contracts.MessageSent{MessageID: d.nextID(), Author: AuthorScheduler, Text: results},
	}); err != nil {
		d.auditLog("schedule.results.record.failed", map[string]any{"threadId": string(id), "error": err.Error()})
	}

	if d.orch == nil {
		d.auditLog("schedule.trigger.failed", map[string]any{"threadId": string(id), "error": "orchestrator is unavailable"})
		return
	}
	stopReports := d.startStatusReports(turnCtx, id)
	defer stopReports()
	if err := d.orch.RunTurn(turnCtx, id, prompt); err != nil {
		if turnCtx.Err() == nil {
			d.auditLog("schedule.turn.failed", map[string]any{"threadId": string(id), "error": err.Error()})
		}
	}
}

// scheduleDepFinished reports whether a dependency thread has done its work:
// it is gone (a deleted dependency owes nothing more), or its current turn
// has ended with every agent settled. A thread that never ran, and one whose
// turn is still open — even in the window between its prompt being recorded
// and its lead being dispatched — is not finished.
func scheduleDepFinished(st *domain.ThreadState) bool {
	if !st.Exists {
		return true
	}
	if st.Turn == 0 {
		return false
	}
	for _, a := range st.Agents {
		if !a.Status.Terminal() {
			return false
		}
	}
	for i := range st.Turns {
		if st.Turns[i].Turn == st.Turn {
			return !st.Turns[i].EndedAt.IsZero()
		}
	}
	return false
}

// scheduleResults compiles the RESULTS section every triggered schedule
// carries: per dependency, its title, thread id, the directory its agents ran
// in, and the messages its final turn produced. The paths are readable on
// disk, so the triggered lead can go deeper than the inline summary.
func (d *Dispatcher) scheduleResults(ctx context.Context, deps []contracts.ThreadID) string {
	var b strings.Builder
	b.WriteString("RESULTS: every thread you were scheduled after has finished. This is what each of them left behind:\n")
	for _, dep := range deps {
		st, err := d.engine.State(ctx, dep)
		if err != nil || !st.Exists {
			fmt.Fprintf(&b, "\n### %s\n(deleted thread; no results were recorded)\n", dep)
			continue
		}
		fmt.Fprintf(&b, "\n### %s (thread %s)\n", st.Title, dep)
		fmt.Fprintf(&b, "Worktree: %s\n", d.depWorkdir(st))
		messages := finalTurnMessages(st)
		if len(messages) == 0 {
			b.WriteString("(its turn recorded no final message)\n")
			continue
		}
		for _, m := range messages {
			fmt.Fprintf(&b, "[%s] %s\n", m.Author, m.Text)
		}
	}
	b.WriteString("\nThe worktrees above are readable on disk, so their full output can be inspected where it lives.\n")
	return b.String()
}

// depWorkdir is the directory a dependency's agents ran in: its worktree when
// it has one, otherwise its project's path or the daemon default.
func (d *Dispatcher) depWorkdir(st *domain.ThreadState) string {
	dir := d.stateWorkdir(st)
	if dir == "" {
		return "(none recorded)"
	}
	return dir
}

// finalTurnMessages returns a thread's messages after its last user message —
// the transcript of the turn that just finished, which is what a dependent
// thread should see.
func finalTurnMessages(st *domain.ThreadState) []domain.Message {
	last := -1
	for i, m := range st.Messages {
		if m.Author == "user" {
			last = i
		}
	}
	if last == -1 {
		return nil
	}
	return st.Messages[last+1:]
}
