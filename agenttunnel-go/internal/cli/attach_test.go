package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestParseAttachCommand(t *testing.T) {
	cases := []struct {
		line   string
		action attachAction
		text   string
	}{
		{line: "/stop", action: attachStop},
		{line: "  /stop  ", action: attachStop},
		{line: "/detach", action: attachDetach},
		{line: "/help", action: attachHelp},
		{line: "please fix the failing test", action: attachSteer, text: "please fix the failing test"},
		{line: "  spaced text  ", action: attachSteer, text: "spaced text"},
		// An unknown slash command is steering text, not a swallowed typo.
		{line: "/usr/bin/env is broken", action: attachSteer, text: "/usr/bin/env is broken"},
	}
	for _, tc := range cases {
		got := parseAttachCommand(tc.line)
		if got.action != tc.action || got.text != tc.text {
			t.Fatalf("parseAttachCommand(%q) = %+v, want %+v", tc.line, got, tc)
		}
	}
}

func TestHandleAttachLineSteersDetachesAndStops(t *testing.T) {
	fake := &recordingCaller{results: map[string]string{
		"thread.steer":     `{"ok":true}`,
		"thread.turn.stop": `{"stopped":true,"ok":true}`,
	}}
	env, out := newUsageTestEnv()
	row := threadListRow{ID: "th_1", Title: "job"}

	done, pending, err := handleAttachLine(context.Background(), env, fake, row, "tighten the loop", nil)
	if err != nil || done || len(pending) != 0 {
		t.Fatalf("steer: done=%v pending=%v err=%v", done, pending, err)
	}
	steer := fake.callFor(t, "thread.steer")
	if steer.params["threadId"] != "th_1" || steer.params["text"] != "tighten the loop" {
		t.Fatalf("steer params = %+v", steer.params)
	}
	if !strings.Contains(out.String(), "steering sent") {
		t.Fatalf("steer output = %q", out.String())
	}

	done, _, err = handleAttachLine(context.Background(), env, fake, row, "/stop", nil)
	if err != nil || done {
		t.Fatalf("/stop: done=%v err=%v", done, err)
	}
	if stop := fake.callFor(t, "thread.turn.stop"); stop.params["threadId"] != "th_1" {
		t.Fatalf("stop params = %+v", stop.params)
	}

	// /detach leaves without stopping: no turn.stop is issued.
	before := len(fake.calls)
	done, _, err = handleAttachLine(context.Background(), env, fake, row, "/detach", nil)
	if err != nil || !done {
		t.Fatalf("/detach: done=%v err=%v", done, err)
	}
	for _, call := range fake.calls[before:] {
		if call.method == "thread.turn.stop" {
			t.Fatal("/detach must not stop the turn")
		}
	}

	// /help lists the commands and keeps the session open.
	envHelp, outHelp := newUsageTestEnv()
	done, _, err = handleAttachLine(context.Background(), envHelp, fake, row, "/help", nil)
	if err != nil || done {
		t.Fatalf("/help: done=%v err=%v", done, err)
	}
	for _, want := range []string{"/stop", "/detach", "/help"} {
		if !strings.Contains(outHelp.String(), want) {
			t.Fatalf("/help output missing %q:\n%s", want, outHelp.String())
		}
	}
}

func TestHandleAttachLineAnswersAnOpenQuestion(t *testing.T) {
	fake := &recordingCaller{results: map[string]string{"question.answer": `{"ok":true}`}}
	env, out := newUsageTestEnv()
	row := threadListRow{ID: "th_1"}
	pending := []questionPromptEntry{{id: "q1", kind: contracts.QuestionChoice, options: []string{"postgres", "sqlite"}}}

	// While a question is open, a typed number is an answer, not a steer.
	done, remaining, err := handleAttachLine(context.Background(), env, fake, row, "2", pending)
	if err != nil || done || len(remaining) != 0 {
		t.Fatalf("answer: done=%v remaining=%v err=%v", done, remaining, err)
	}
	answer := fake.callFor(t, "question.answer")
	if answer.params["questionId"] != "q1" {
		t.Fatalf("answer params = %+v", answer.params)
	}
	selected, _ := answer.params["selected"].([]any)
	if len(selected) != 1 || selected[0] != "sqlite" {
		t.Fatalf("selected = %v, want sqlite", answer.params["selected"])
	}
	if _, ok := answer.params["text"]; ok {
		t.Fatalf("a numeric choice must not also send text: %+v", answer.params)
	}
	_ = out
}

func TestHandleAttachLineFreeTextAnswer(t *testing.T) {
	fake := &recordingCaller{results: map[string]string{"question.answer": `{"ok":true}`}}
	env, _ := newUsageTestEnv()
	row := threadListRow{ID: "th_1"}
	pending := []questionPromptEntry{{id: "q2", kind: contracts.QuestionText}}

	if _, _, err := handleAttachLine(context.Background(), env, fake, row, "use the staging database", pending); err != nil {
		t.Fatalf("free text: %v", err)
	}
	answer := fake.callFor(t, "question.answer")
	if answer.params["text"] != "use the staging database" {
		t.Fatalf("text = %v", answer.params["text"])
	}
	if _, ok := answer.params["selected"]; ok {
		t.Fatalf("free text must not send a selection: %+v", answer.params)
	}
}

func TestSelectOptions(t *testing.T) {
	choice := questionPromptEntry{kind: contracts.QuestionChoice, options: []string{"a", "b", "c"}}
	if got, ok := selectOptions(choice, "3"); !ok || len(got) != 1 || got[0] != "c" {
		t.Fatalf("choice 3 = %v, %v", got, ok)
	}
	// Out of range falls back to free text rather than being swallowed.
	if _, ok := selectOptions(choice, "9"); ok {
		t.Fatal("out-of-range number must not select")
	}
	// A choice question takes one number, not a list.
	if _, ok := selectOptions(choice, "1,2"); ok {
		t.Fatal("choice must not take a comma list")
	}
	multi := questionPromptEntry{kind: contracts.QuestionMulti, options: []string{"a", "b", "c"}}
	if got, ok := selectOptions(multi, "1,3"); !ok || strings.Join(got, ",") != "a,c" {
		t.Fatalf("multi 1,3 = %v, %v", got, ok)
	}
	// A text question never selects, even when it carries options.
	text := questionPromptEntry{kind: contracts.QuestionText, options: []string{"a"}}
	if _, ok := selectOptions(text, "1"); ok {
		t.Fatal("text questions take free text only")
	}
}

func TestAttachEventSurfacesQuestions(t *testing.T) {
	env, out := newUsageTestEnv()
	frame := func(payload any) Event {
		raw, err := json.Marshal(map[string]any{
			"threadId": "th_1", "type": "thread.question-asked",
			"payload": payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		return Event{Method: "thread.event", Params: raw}
	}

	asked, closed := handleAttachEvent(env, frame(contracts.QuestionAsked{
		QuestionID: "q1", Kind: contracts.QuestionChoice,
		Question: "which database?", Options: []string{"postgres", "sqlite"},
	}), "th_1", newLogDeduper(nil), newTurnTracker(map[string]contracts.AgentStatus{}, false))
	if asked == nil || asked.id != "q1" || len(asked.options) != 2 {
		t.Fatalf("asked = %+v", asked)
	}
	if closed != "" {
		t.Fatalf("closed = %q", closed)
	}

	// The printer numbers the options so "1"/"2" is an unambiguous answer.
	printQuestion(env, *asked)
	got := out.String()
	for _, want := range []string{"which database?", "1) postgres", "2) sqlite"} {
		if !strings.Contains(got, want) {
			t.Fatalf("question prompt missing %q:\n%s", want, got)
		}
	}

	closedRaw, _ := json.Marshal(map[string]any{
		"threadId": "th_1", "type": "thread.question-closed",
		"payload": map[string]any{"questionId": "q1", "answered": true},
	})
	_, closedID := handleAttachEvent(env, Event{Method: "thread.event", Params: closedRaw}, "th_1",
		newLogDeduper(nil), newTurnTracker(map[string]contracts.AgentStatus{}, false))
	if closedID != "q1" {
		t.Fatalf("closed = %q, want q1", closedID)
	}
}

func TestAttachIgnoresOtherThreadsEvents(t *testing.T) {
	env, out := newUsageTestEnv()
	raw, _ := json.Marshal(map[string]any{
		"threadId": "th_other", "type": "agent.output",
		"payload": map[string]any{"agentId": "a1", "kind": "text", "text": "not mine"},
	})
	if _, _ = handleAttachEvent(env, Event{Method: "thread.event", Params: raw}, "th_1",
		newLogDeduper(nil), newTurnTracker(map[string]contracts.AgentStatus{}, false)); len(out.String()) != 0 {
		t.Fatalf("another thread's output must not print:\n%s", out.String())
	}
}

func TestParseStopArgs(t *testing.T) {
	opts, err := parseStopArgs([]string{"th_1", "--agents"})
	if err != nil || opts.ref != "th_1" || !opts.agents {
		t.Fatalf("opts = %+v, %v", opts, err)
	}
	for _, bad := range [][]string{{}, {"a", "b"}, {"--bogus", "t"}} {
		if _, err := parseStopArgs(bad); err == nil {
			t.Fatalf("parseStopArgs(%v) should fail", bad)
		}
	}
}

func TestStopAgentsStopsOnlyRunningAgents(t *testing.T) {
	fake := &recordingCaller{results: map[string]string{
		"agents.list": `[
			{"agent":{"id":"agent-orchestrator","status":"running"}},
			{"agent":{"id":"agent-exec","status":"succeeded"}},
			{"agent":{"id":"agent-pending","status":"pending"}}
		]`,
		"agents.stop": `{"ok":true,"stopped":true}`,
	}}
	env, out := newUsageTestEnv()
	if err := stopAgents(context.Background(), env, fake, "th_1"); err != nil {
		t.Fatalf("stopAgents: %v", err)
	}
	var stopped []string
	for _, call := range fake.calls {
		if call.method == "agents.stop" {
			stopped = append(stopped, call.params["agentId"].(string))
		}
	}
	// Running and pending agents are stopped; a terminal one is left alone.
	if strings.Join(stopped, ",") != "agent-orchestrator,agent-pending" && strings.Join(stopped, ",") != "agent-pending,agent-orchestrator" {
		t.Fatalf("stopped = %v", stopped)
	}
	if !strings.Contains(out.String(), "stopped 2 agent(s) on th_1") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestStopAgentsWithNothingRunning(t *testing.T) {
	fake := &recordingCaller{results: map[string]string{
		"agents.list": `[{"agent":{"id":"a","status":"succeeded"}}]`,
	}}
	env, out := newUsageTestEnv()
	if err := stopAgents(context.Background(), env, fake, "th_1"); err != nil {
		t.Fatalf("stopAgents: %v", err)
	}
	for _, call := range fake.calls {
		if call.method == "agents.stop" {
			t.Fatalf("nothing is running, so nothing should be stopped: %v", call)
		}
	}
	if !strings.Contains(out.String(), "no running agents") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestPrintTurnStop(t *testing.T) {
	env, out := newUsageTestEnv()
	printTurnStop(env, "th_1", true)
	if !strings.Contains(out.String(), "stopped turn on th_1") {
		t.Fatalf("output = %q", out.String())
	}
	env2, out2 := newUsageTestEnv()
	printTurnStop(env2, "th_2", false)
	if !strings.Contains(out2.String(), "has no running turn") {
		t.Fatalf("output = %q", out2.String())
	}
}
