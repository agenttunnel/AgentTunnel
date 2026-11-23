package domain

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// startTurn opens a turn whose anchor is msgID and whose baseline is ckID.
func startTurn(msgID, ckID, baseRef, baseTree string) contracts.Command {
	return contracts.Command{
		ID: msgID, Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "do the thing",
		Checkpoint: &contracts.CheckpointCapture{CheckpointID: ckID, Phase: contracts.CheckpointBaseline, Ref: baseRef, Tree: baseTree},
	}
}

func resultFor(ckID, ref, tree string, files, add, del int) contracts.Command {
	return contracts.Command{
		Type: contracts.CmdCheckpointCapture, ThreadID: "t1",
		Checkpoint: &contracts.CheckpointCapture{CheckpointID: ckID, Phase: contracts.CheckpointResult, Ref: ref, Tree: tree, Files: files, Additions: add, Deletions: del},
	}
}

func newCheckpointThread(t *testing.T) *ThreadState {
	t.Helper()
	s := NewThreadState("t1")
	s.Exists = true
	return s
}

func TestBaselineThenResultMergeIntoOneCheckpoint(t *testing.T) {
	s := newCheckpointThread(t)
	if err := apply(s, startTurn("m1", "ck_a1", "c-base", "tree-base")); err != nil {
		t.Fatalf("turn start: %v", err)
	}
	if len(s.Checkpoints) != 1 {
		t.Fatalf("checkpoints = %d after baseline, want 1", len(s.Checkpoints))
	}
	cp := s.Checkpoints[0]
	if cp.Turn != 1 || cp.AnchorMessageID != "m1" || cp.BaselineTree != "tree-base" || cp.Ready {
		t.Fatalf("baseline checkpoint = %+v", cp)
	}

	if err := apply(s, resultFor("ck_a1", "c-res", "tree-res", 2, 3, 1)); err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(s.Checkpoints) != 1 {
		t.Fatalf("result added a checkpoint: %d", len(s.Checkpoints))
	}
	cp = s.Checkpoints[0]
	if !cp.Ready || cp.Tree != "tree-res" || cp.Files != 2 || cp.Additions != 3 || cp.Deletions != 1 || cp.BaselineTree != "tree-base" {
		t.Fatalf("finished checkpoint = %+v", cp)
	}
}

func TestFailedResultKeepsCheckpointNotReady(t *testing.T) {
	s := newCheckpointThread(t)
	if err := apply(s, startTurn("m1", "ck_a1", "c-base", "tree-base")); err != nil {
		t.Fatal(err)
	}
	failed := resultFor("ck_a1", "", "", 0, 0, 0)
	failed.Checkpoint.Error = "git add: permission denied"
	if err := apply(s, failed); err != nil {
		t.Fatalf("failed result: %v", err)
	}
	cp := s.Checkpoints[0]
	if cp.Ready || cp.Error == "" {
		t.Fatalf("failed checkpoint should be not ready with an error: %+v", cp)
	}
	// Events are immutable: a second result for the same checkpoint is refused.
	if err := apply(s, resultFor("ck_a1", "c-res", "tree-res", 1, 1, 0)); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("second result: err = %v, want ErrInvalidCommand", err)
	}
}

func TestCheckpointCaptureRejectsForgedAndOutOfOrderCommands(t *testing.T) {
	s := newCheckpointThread(t)
	if err := apply(s, startTurn("m1", "ck_a1", "c-base", "tree-base")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]contracts.Command{
		"path-shaped id":     startTurn("m2", "../escape", "c", "t"),
		"unknown result":     resultFor("ck_zz", "c", "t", 0, 0, 0),
		"baseline capture":   {Type: contracts.CmdCheckpointCapture, ThreadID: "t1", Checkpoint: &contracts.CheckpointCapture{CheckpointID: "ck_a1", Phase: contracts.CheckpointBaseline, Ref: "c", Tree: "t"}},
		"reused baseline id": startTurn("m2", "ck_a1", "c", "t"),
	}
	for name, cmd := range cases {
		if _, err := Decide(cmd, s); !errors.Is(err, ErrInvalidCommand) {
			t.Errorf("%s: err = %v, want ErrInvalidCommand", name, err)
		}
	}
	// A refused turn leaves no message behind.
	before := len(s.Messages)
	if err := apply(s, startTurn("m2", "../escape", "c", "t")); err == nil {
		t.Fatal("turn with a forged checkpoint id was accepted")
	}
	if len(s.Messages) != before {
		t.Fatalf("refused turn left %d messages", len(s.Messages)-before)
	}
}

func TestRewindTruncatesMessagesTurnsAndCheckpoints(t *testing.T) {
	s := newCheckpointThread(t)
	for i, id := range []string{"m1", "m2", "m3"} {
		ck := "ck_" + string(rune('a'+i))
		if err := apply(s, startTurn(id, ck, "base-"+id, "tree-"+id)); err != nil {
			t.Fatal(err)
		}
		if err := apply(s, contracts.Command{ID: "reply-" + id, Type: contracts.CmdMessageAppend, ThreadID: "t1",
			Message: &contracts.MessageSent{MessageID: "reply-" + id, Author: "orchestrator", Text: "done"}}); err != nil {
			t.Fatal(err)
		}
		if err := apply(s, resultFor(ck, "res-"+id, "rtree-"+id, 1, 1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if s.Turn != 3 || len(s.Checkpoints) != 3 {
		t.Fatalf("setup: turn %d, checkpoints %d", s.Turn, len(s.Checkpoints))
	}

	if err := apply(s, contracts.Command{Type: contracts.CmdThreadRewind, ThreadID: "t1", Rewind: &contracts.RewindRequest{ToMessageID: "m2"}}); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if s.Turn != 1 {
		t.Fatalf("turn = %d after rewind to turn 2, want 1", s.Turn)
	}
	var ids []string
	for _, m := range s.Messages {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"m1", "reply-m1"}) {
		t.Fatalf("messages after rewind = %v", ids)
	}
	if len(s.Checkpoints) != 1 || s.Checkpoints[0].ID != "ck_a" {
		t.Fatalf("checkpoints after rewind = %+v", s.Checkpoints)
	}
	if len(s.Turns) != 1 || s.Turns[0].Turn != 1 {
		t.Fatalf("turn usage after rewind = %+v", s.Turns)
	}
}

func TestRewindToFirstTurnResetsTurnToZero(t *testing.T) {
	s := newCheckpointThread(t)
	if err := apply(s, startTurn("m1", "ck_a", "base", "tree")); err != nil {
		t.Fatal(err)
	}
	if err := apply(s, contracts.Command{Type: contracts.CmdThreadRewind, ThreadID: "t1", Rewind: &contracts.RewindRequest{ToMessageID: "m1"}}); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if s.Turn != 0 || len(s.Messages) != 0 || len(s.Checkpoints) != 0 || len(s.Turns) != 0 {
		t.Fatalf("thread not fresh after rewinding to the first turn: turn=%d msgs=%d cks=%d turns=%d",
			s.Turn, len(s.Messages), len(s.Checkpoints), len(s.Turns))
	}
	// The next turn is turn 1 again, as on a fresh thread.
	if err := apply(s, startTurn("m9", "ck_b", "base2", "tree2")); err != nil {
		t.Fatal(err)
	}
	if s.Turn != 1 || s.Checkpoints[0].Turn != 1 {
		t.Fatalf("next turn after reset = %d / %d, want 1", s.Turn, s.Checkpoints[0].Turn)
	}
}

func TestRewindRefusesNonAnchorBusyAndUnrestorableTargets(t *testing.T) {
	s := newCheckpointThread(t)
	if err := apply(s, startTurn("m1", "ck_a", "base", "tree")); err != nil {
		t.Fatal(err)
	}
	if err := apply(s, contracts.Command{ID: "r1", Type: contracts.CmdMessageAppend, ThreadID: "t1",
		Message: &contracts.MessageSent{MessageID: "r1", Author: "orchestrator", Text: "ok"}}); err != nil {
		t.Fatal(err)
	}

	// Steering and reports never open a turn, so they are not rewind targets.
	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadRewind, ThreadID: "t1", Rewind: &contracts.RewindRequest{ToMessageID: "r1"}}, s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("report target: err = %v, want ErrInvalidCommand", err)
	}
	// An agent still running blocks the rewind.
	s.Agents["lead"] = contracts.Agent{ID: "lead", Status: contracts.AgentRunning}
	if _, err := Decide(contracts.Command{Type: contracts.CmdThreadRewind, ThreadID: "t1", Rewind: &contracts.RewindRequest{ToMessageID: "m1"}}, s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("busy thread: err = %v, want ErrInvalidCommand", err)
	}
	s.Agents["lead"] = contracts.Agent{ID: "lead", Status: contracts.AgentSucceeded}

	// A file restore needs a baseline for the target turn.
	s.Checkpoints = nil
	_, err := Decide(contracts.Command{Type: contracts.CmdThreadRewind, ThreadID: "t1",
		Rewind: &contracts.RewindRequest{ToMessageID: "m1", RestoreFiles: true, SafetyRef: "x"}}, s)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("restore without a checkpoint: err = %v, want ErrInvalidCommand", err)
	}
}

func TestCheckpointReplayMatchesLiveState(t *testing.T) {
	live := newCheckpointThread(t)
	var log []contracts.Event
	record := func(cmd contracts.Command) {
		t.Helper()
		evs, err := Decide(cmd, live)
		if err != nil {
			t.Fatalf("decide %s: %v", cmd.Type, err)
		}
		for _, p := range evs {
			ev := contracts.Event{Seq: int64(len(log) + 1), ThreadID: "t1", At: reviewAt, Payload: p}
			log = append(log, ev)
			if err := Project(live, ev); err != nil {
				t.Fatalf("project: %v", err)
			}
		}
	}
	record(startTurn("m1", "ck_a", "base1", "tree1"))
	record(resultFor("ck_a", "res1", "rtree1", 1, 2, 0))
	record(startTurn("m2", "ck_b", "base2", "tree2"))
	record(contracts.Command{Type: contracts.CmdThreadRewind, ThreadID: "t1", Rewind: &contracts.RewindRequest{ToMessageID: "m2"}})

	replayed := newCheckpointThread(t)
	for _, ev := range log {
		if err := Project(replayed, ev); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := mustJSON(t, replayed.Checkpoints), mustJSON(t, live.Checkpoints); got != want {
		t.Fatalf("replayed checkpoints differ:\n got %s\nwant %s", got, want)
	}
	if got, want := mustJSON(t, replayed.Messages), mustJSON(t, live.Messages); got != want {
		t.Fatalf("replayed messages differ")
	}
	if replayed.Turn != live.Turn {
		t.Fatalf("replayed turn %d, live %d", replayed.Turn, live.Turn)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
