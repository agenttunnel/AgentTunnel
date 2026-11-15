package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// newOwnerFixture wires a dispatcher whose runner can run a full turn over a
// fake harness, so the owner contract can be exercised end to end without git
// or a real provider CLI.
func newOwnerFixture(t *testing.T) (*domain.Engine, *Dispatcher) {
	t.Helper()
	ctx := context.Background()
	// The engine owns the turn's background writers, and t.Cleanup runs LIFO:
	// create the directories those writers append to first and register the
	// engine's shutdown last, so it closes before t.TempDir removes them.
	// Engine.Close joins the worker loop, so no event can be appended after it
	// returns (see domain.Engine.Close); without the ordering a late
	// background write lands in a directory already being removed and the
	// cleanup fails with "directory not empty".
	logsDir := t.TempDir()
	inheritedDir := t.TempDir()

	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		return providers.CommandSpec{
			Binary:    "sh",
			Args:      []string{"-c", `printf '{"type":"text","text":"DONE"}\n'`, spec.Prompt},
			PromptVia: "arg",
		}, nil
	}, providers.JSONLineTranslator{})
	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:    eng,
		Registry:  providers.NewRegistry(driver),
		Roles:     contracts.NewRoleRegistry(contracts.BuiltinRoles()...),
		Logs:      logstore.New(logsDir),
		Ledger:    usage.NewLedger(),
		Mailbox:   contextstore.NewMailbox(),
		Inherited: contextstore.NewInheritedContext(inheritedDir),
	})
	// Production keeps the runtime mailbox always on and gates on the durable
	// peer-mailbox flag instead (see pkg/daemon); mirror that so the
	// subthread's report flows like it does in the real daemon.
	runner.Mailbox().SetEnabled(true)
	return eng, NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner, Orchestrator: orchestrator.New(runner)})
}

func ownerRPC(t *testing.T, disp *Dispatcher, method string, params map[string]any) any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	out, err := disp.Handle(context.Background(), Request{Method: method, Params: raw})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

// TestThreadCreateSurfacesUserOwner covers the user side of the owner wire
// contract: a thread started through the thread.create RPC reads as user-owned
// in both the list summary and the full thread state. A client cannot forge
// the owner through the create params — only creation sites decide it.
func TestThreadCreateSurfacesUserOwner(t *testing.T) {
	_, disp := newOwnerFixture(t)

	created := ownerRPC(t, disp, "thread.create", map[string]any{
		"title": "user thread", "projectId": "proj-owner", "owner": "orchestrator",
	}).(map[string]any)
	id := created["threadId"].(contracts.ThreadID)

	// thread.list reports the creator.
	threads := ownerRPC(t, disp, "thread.list", nil).([]ThreadSummary)
	if len(threads) != 1 {
		t.Fatalf("thread.list = %d entries, want 1", len(threads))
	}
	if got := threads[0].ID; got != id {
		t.Fatalf("thread.list id = %q, want %q", got, id)
	}
	if got := threads[0].Owner; got != contracts.ThreadOwnerUser {
		t.Fatalf("thread.list owner = %q, want %q (and params must not forge one)", got, contracts.ThreadOwnerUser)
	}

	// thread.get returns the state directly and carries the same owner.
	st := ownerRPC(t, disp, "thread.get", map[string]any{"threadId": id}).(*domain.ThreadState)
	if st.Owner != contracts.ThreadOwnerUser {
		t.Fatalf("thread.get owner = %q, want %q", st.Owner, contracts.ThreadOwnerUser)
	}

	// Pin the literal wire key, not just the Go field: the desktop owner chip
	// reads "owner" from raw JSON, and renaming the json tag would keep every
	// struct-level assertion green while the wire (and the chip) silently
	// changes. A tag rename has bit this repo before.
	listRaw, err := json.Marshal(threads)
	if err != nil {
		t.Fatal(err)
	}
	var listRows []map[string]any
	if err := json.Unmarshal(listRaw, &listRows); err != nil {
		t.Fatal(err)
	}
	if len(listRows) != 1 {
		t.Fatalf("raw thread.list rows = %d, want 1", len(listRows))
	}
	if got, ok := listRows[0]["owner"]; !ok || got != "user" {
		t.Fatalf(`raw thread.list row owner = %v (present=%t), want key "owner" = "user"`, got, ok)
	}

	stateRaw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var stateRow map[string]any
	if err := json.Unmarshal(stateRaw, &stateRow); err != nil {
		t.Fatal(err)
	}
	if got, ok := stateRow["owner"]; !ok || got != "user" {
		t.Fatalf(`raw thread.get state owner = %v (present=%t), want key "owner" = "user"`, got, ok)
	}

	// omitempty means a pre-change thread (empty owner) serializes WITHOUT the
	// key; the frontend reads absence as user. Work on copies so a zeroed
	// owner cannot leak back into the assertions above.
	legacy := threads[0]
	legacy.Owner = ""
	legacyRaw, err := json.Marshal([]ThreadSummary{legacy})
	if err != nil {
		t.Fatal(err)
	}
	var legacyRows []map[string]any
	if err := json.Unmarshal(legacyRaw, &legacyRows); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacyRows[0]["owner"]; ok {
		t.Fatal(`legacy (empty-owner) thread.list row still carries the "owner" key; omitempty broken`)
	}

	legacyState := st.Clone()
	legacyState.Owner = ""
	legacyStateRaw, err := json.Marshal(legacyState)
	if err != nil {
		t.Fatal(err)
	}
	var legacyStateRow map[string]any
	if err := json.Unmarshal(legacyStateRaw, &legacyStateRow); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacyStateRow["owner"]; ok {
		t.Fatal(`legacy (empty-owner) thread.get state still carries the "owner" key; omitempty broken`)
	}
}

// TestSubthreadOwnerIsOrchestrator covers the point of the feature: a subthread
// started by the parent's orchestrator is recorded as orchestrator-owned, in the
// child state and in the thread list.
func TestSubthreadOwnerIsOrchestrator(t *testing.T) {
	eng, disp := newOwnerFixture(t)

	created := ownerRPC(t, disp, "thread.create", map[string]any{
		"title": "parent", "projectId": "proj-owner",
	}).(map[string]any)
	parent := created["threadId"].(contracts.ThreadID)

	// Watch every thread, not just the parent: the child id only exists in
	// startSubthread's reply, and a child event can commit before the parent is
	// subscribed to it.
	events, unsub := eng.SubscribeAll()
	defer unsub()

	child, err := disp.startSubthread(context.Background(), parent, "write the release notes")
	if err != nil {
		t.Fatalf("startSubthread: %v", err)
	}

	// The owner is durable at creation, before the background turn reports.
	st, err := eng.State(context.Background(), child)
	if err != nil {
		t.Fatal(err)
	}
	if st.Owner != contracts.ThreadOwnerOrchestrator {
		t.Fatalf("subthread owner = %q, want %q", st.Owner, contracts.ThreadOwnerOrchestrator)
	}

	threads := ownerRPC(t, disp, "thread.list", nil).([]ThreadSummary)
	owners := map[contracts.ThreadID]contracts.ThreadOwner{}
	for _, ts := range threads {
		owners[ts.ID] = ts.Owner
	}
	if got := owners[parent]; got != contracts.ThreadOwnerUser {
		t.Fatalf("parent owner = %q, want %q", got, contracts.ThreadOwnerUser)
	}
	if got := owners[child]; got != contracts.ThreadOwnerOrchestrator {
		t.Fatalf("child owner = %q, want %q", got, contracts.ThreadOwnerOrchestrator)
	}

	// Pin the literal wire keys on the raw JSON rows, so a json tag rename
	// cannot slip past the struct-level assertions above and silently break
	// desktop consumers that read "owner" from the wire.
	listRaw, err := json.Marshal(threads)
	if err != nil {
		t.Fatal(err)
	}
	var listRows []map[string]any
	if err := json.Unmarshal(listRaw, &listRows); err != nil {
		t.Fatal(err)
	}
	rowsByID := map[string]map[string]any{}
	for _, row := range listRows {
		if id, ok := row["id"].(string); ok {
			rowsByID[id] = row
		}
	}
	if got, ok := rowsByID[string(parent)]["owner"]; !ok || got != "user" {
		t.Fatalf(`raw parent row owner = %v (present=%t), want key "owner" = "user"`, got, ok)
	}
	if got, ok := rowsByID[string(child)]["owner"]; !ok || got != "orchestrator" {
		t.Fatalf(`raw child row owner = %v (present=%t), want key "owner" = "orchestrator"`, got, ok)
	}

	childRaw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var childRow map[string]any
	if err := json.Unmarshal(childRaw, &childRow); err != nil {
		t.Fatal(err)
	}
	if got, ok := childRow["owner"]; !ok || got != "orchestrator" {
		t.Fatalf(`raw child state owner = %v (present=%t), want key "owner" = "orchestrator"`, got, ok)
	}

	// Wait for the background turn to quiesce before the test ends, so no
	// writer outlives it. Both markers are past the tool call that returned the
	// child id:
	//   - the parent transcript entry reportSubthread appends (author
	//     "subthread"). It lands after PostMessage's MailboxPosted event and
	//     after that function's logstore write, which is the write that used to
	//     race the temp-directory cleanup;
	//   - the child's second rename. RunTurn starts the summary-informed title
	//     goroutine after the turn and joins nothing, so only a second
	//     ThreadRenamed on the child proves that writer is finished. (The fake
	//     harness always answers the title prompt with "DONE", so both
	//     generations rename deterministically.)
	// Either marker can commit last, so both are awaited in one loop; the
	// deadline is a failure timeout, never a wait.
	deadline := time.After(20 * time.Second)
	reported, renames := false, 0
	for !reported || renames < 2 {
		select {
		case ev := <-events:
			switch p := ev.Payload.(type) {
			case contracts.MessageSent:
				if ev.ThreadID == parent && p.Author == string(subthreadSender) {
					reported = true
				}
			case contracts.ThreadRenamed:
				if ev.ThreadID == child {
					renames++
				}
			}
		case <-deadline:
			end, _ := eng.State(context.Background(), parent)
			childEnd, _ := eng.State(context.Background(), child)
			t.Fatalf("background turn did not settle (parent report=%t, child renames=%d); parent messages: %+v; child messages: %+v",
				reported, renames, end.Messages, childEnd.Messages)
		}
	}
}
