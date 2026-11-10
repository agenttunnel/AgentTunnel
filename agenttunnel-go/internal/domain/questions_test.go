package domain

import (
	"errors"
	"reflect"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func askCmd(id string, kind contracts.QuestionKind, options ...string) contracts.Command {
	return contracts.Command{Type: contracts.CmdQuestionAsk, ThreadID: "t1", QuestionAsk: &contracts.QuestionAsked{
		QuestionID: id, AgentID: "a1", Kind: kind, Question: "Which way?", Options: options,
	}}
}

func closeCmd(id string, answered bool, selected []string, text, reason string) contracts.Command {
	return contracts.Command{Type: contracts.CmdQuestionClose, ThreadID: "t1", QuestionClose: &contracts.QuestionClosed{
		QuestionID: id, Answered: answered, Selected: selected, Text: text, Reason: reason,
	}}
}

// withPendingQuestion applies a decided ask to a fresh thread so close validation
// has a pending question to read.
func withPendingQuestion(t *testing.T, cmd contracts.Command) *ThreadState {
	t.Helper()
	s := existingThread()
	evs, err := Decide(cmd, s)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	for _, p := range evs {
		if err := Project(s, contracts.Event{ThreadID: "t1", Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestDecideQuestionAskRejectsBadShapes(t *testing.T) {
	s := existingThread()
	for name, cmd := range map[string]contracts.Command{
		"no payload":       {Type: contracts.CmdQuestionAsk, ThreadID: "t1"},
		"blank question":   {Type: contracts.CmdQuestionAsk, ThreadID: "t1", QuestionAsk: &contracts.QuestionAsked{QuestionID: "q1", Kind: contracts.QuestionText, Question: "  "}},
		"unknown kind":     askCmd("q1", "dropdown", "a", "b"),
		"no id":            {Type: contracts.CmdQuestionAsk, ThreadID: "t1", QuestionAsk: &contracts.QuestionAsked{Kind: contracts.QuestionText, Question: "why?"}},
		"choice one opt":   askCmd("q1", contracts.QuestionChoice, "only"),
		"choice 13 opts":   askCmd("q1", contracts.QuestionChoice, manyOptions(13)...),
		"duplicate option": askCmd("q1", contracts.QuestionMulti, "a", "a"),
		"blank option":     askCmd("q1", contracts.QuestionChoice, "a", "  "),
		"text with opts":   askCmd("q1", contracts.QuestionText, "a", "b"),
	} {
		if _, err := Decide(cmd, s); !errors.Is(err, ErrInvalidCommand) {
			t.Errorf("%s: err = %v, want ErrInvalidCommand", name, err)
		}
	}
}

func TestDecideQuestionAskRefusesDuplicatesAndPileUps(t *testing.T) {
	s := withPendingQuestion(t, askCmd("q1", contracts.QuestionText))
	if _, err := Decide(askCmd("q1", contracts.QuestionText), s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("duplicate id err = %v, want ErrInvalidCommand", err)
	}
	for i := 2; i <= contracts.MaxQuestionPending; i++ {
		evs, err := Decide(askCmd(string(rune('a'+i)), contracts.QuestionText), s)
		if err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
		_ = Project(s, contracts.Event{ThreadID: "t1", Payload: evs[0]})
	}
	if _, err := Decide(askCmd("over", contracts.QuestionText), s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("ask past the pending cap err = %v, want ErrInvalidCommand", err)
	}
}

func TestDecideQuestionCloseValidatesTheAnswer(t *testing.T) {
	cases := []struct {
		name string
		ask  contracts.Command
		cls  contracts.Command
		ok   bool
	}{
		{"choice picks an option", askCmd("q", contracts.QuestionChoice, "a", "b"), closeCmd("q", true, []string{"a"}, "", ""), true},
		{"choice other text", askCmd("q", contracts.QuestionChoice, "a", "b"), closeCmd("q", true, nil, "neither", ""), true},
		{"choice rejects an unknown option", askCmd("q", contracts.QuestionChoice, "a", "b"), closeCmd("q", true, []string{"zzz"}, "", ""), false},
		{"choice rejects two options", askCmd("q", contracts.QuestionChoice, "a", "b"), closeCmd("q", true, []string{"a", "b"}, "", ""), false},
		{"choice rejects option plus text", askCmd("q", contracts.QuestionChoice, "a", "b"), closeCmd("q", true, []string{"a"}, "also", ""), false},
		{"choice rejects an empty answer", askCmd("q", contracts.QuestionChoice, "a", "b"), closeCmd("q", true, nil, "", ""), false},
		{"multi picks several", askCmd("q", contracts.QuestionMulti, "a", "b", "c"), closeCmd("q", true, []string{"a", "c"}, "", ""), true},
		{"multi rejects a repeat", askCmd("q", contracts.QuestionMulti, "a", "b"), closeCmd("q", true, []string{"a", "a"}, "", ""), false},
		{"multi rejects nothing", askCmd("q", contracts.QuestionMulti, "a", "b"), closeCmd("q", true, nil, "", ""), false},
		{"text takes text", askCmd("q", contracts.QuestionText), closeCmd("q", true, nil, "use the new schema", ""), true},
		{"text rejects blank", askCmd("q", contracts.QuestionText), closeCmd("q", true, nil, "   ", ""), false},
		{"text rejects selections", askCmd("q", contracts.QuestionText), closeCmd("q", true, []string{"a"}, "x", ""), false},
		{"decline needs a reason", askCmd("q", contracts.QuestionText), closeCmd("q", false, nil, "", ""), false},
		{"decline with reason", askCmd("q", contracts.QuestionText), closeCmd("q", false, nil, "", "declined by user"), true},
	}
	for _, tc := range cases {
		s := withPendingQuestion(t, tc.ask)
		_, err := Decide(tc.cls, s)
		if tc.ok && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidCommand) {
			t.Errorf("%s: err = %v, want ErrInvalidCommand", tc.name, err)
		}
	}
}

func TestDecideQuestionCloseOnlyOnce(t *testing.T) {
	s := withPendingQuestion(t, askCmd("q", contracts.QuestionText))
	evs, err := Decide(closeCmd("q", true, nil, "yes", ""), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range evs {
		if err := Project(s, contracts.Event{ThreadID: "t1", Payload: p}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Decide(closeCmd("q", false, nil, "", "timed out"), s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("second close err = %v, want ErrInvalidCommand", err)
	}
	if _, err := Decide(closeCmd("missing", true, nil, "x", ""), s); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("close of unknown question err = %v, want ErrInvalidCommand", err)
	}
}

// TestQuestionReplayRebuildsTheSameState asks, answers and dismisses through the
// engine, then projects the durable log from scratch. The rebuilt list must match
// the live one, so a restart shows exactly what the caller saw.
func TestQuestionReplayRebuildsTheSameState(t *testing.T) {
	e, ctx := newTestEngine(t)
	mustDispatch(t, e, ctx, contracts.Command{Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	mustDispatch(t, e, ctx, askCmd("q1", contracts.QuestionChoice, "red", "blue"))
	mustDispatch(t, e, ctx, askCmd("q2", contracts.QuestionMulti, "a", "b"))
	mustDispatch(t, e, ctx, closeCmd("q1", true, []string{"blue"}, "", ""))
	mustDispatch(t, e, ctx, closeCmd("q2", false, nil, "", "declined by user"))

	live, err := e.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	events, err := e.Events(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	replayed := NewThreadState("t1")
	for _, ev := range events {
		if err := Project(replayed, ev); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(live.Questions, replayed.Questions) {
		t.Fatalf("replayed questions differ:\n live %+v\n replay %+v", live.Questions, replayed.Questions)
	}
	if got := replayed.Questions[0]; got.Status != QuestionAnswered || !reflect.DeepEqual(got.Selected, []string{"blue"}) {
		t.Fatalf("q1 = %+v, want answered with blue", got)
	}
	if got := replayed.Questions[1]; got.Status != QuestionClosedStatus || got.Reason != "declined by user" {
		t.Fatalf("q2 = %+v, want closed with the decline reason", got)
	}
}

func TestQuestionCloneIsIsolated(t *testing.T) {
	s := withPendingQuestion(t, askCmd("q", contracts.QuestionText))
	c := s.Clone()
	c.Questions[0].Status = QuestionAnswered
	if s.Questions[0].Status != QuestionPending {
		t.Fatal("Clone must not share the question list with the live state")
	}
}

func manyOptions(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(rune('a' + i))
	}
	return out
}
