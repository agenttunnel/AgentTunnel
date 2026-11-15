package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeCLICaller answers RPCs from canned JSON, so renderers run with no daemon.
type fakeCLICaller struct {
	calls   []string
	results map[string]string
	errs    map[string]error
}

func (f *fakeCLICaller) Call(_ context.Context, method string, _ any, out any) error {
	f.calls = append(f.calls, method)
	if err := f.errs[method]; err != nil {
		return err
	}
	raw, ok := f.results[method]
	if !ok {
		return fmt.Errorf("unexpected RPC %s", method)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

func newUsageTestEnv() (*Env, *bytes.Buffer) {
	var out bytes.Buffer
	return &Env{Out: &out, Err: &out}, &out
}

const zeroUsageJSON = `{"orchestrator":{"inputTokens":0,"outputTokens":0,"cachedTokens":0,"costUsd":0,"toolUses":0},"subagents":{"inputTokens":0,"outputTokens":0,"cachedTokens":0,"costUsd":0,"toolUses":0},"byAgent":[]}`

func TestUsageLedgerZeroState(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{
		"usage.get":   zeroUsageJSON,
		"thread.list": `[]`,
	}}
	if err := renderUsage(context.Background(), env, fake, usageOptions{}); err != nil {
		t.Fatalf("renderUsage: %v", err)
	}
	got := out.String()
	for _, want := range []string{"TOTAL  INPUT 0  OUTPUT 0  CACHE 0", "$0.0000", "no agent usage recorded yet"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "AGENT") {
		t.Fatalf("zero state should not print an agent table:\n%s", got)
	}
}

func TestUsageLedgerAgentsAndModels(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{
		"usage.get": `{
			"orchestrator":{"inputTokens":1000,"outputTokens":200,"cachedTokens":50,"costUsd":0.01,"toolUses":3},
			"subagents":{"inputTokens":500,"outputTokens":100,"cachedTokens":0,"costUsd":0.005,"toolUses":1},
			"byAgent":[
				{"agentId":"a-orch","role":"orchestrator","usage":{"inputTokens":1000,"outputTokens":200,"cachedTokens":50,"costUsd":0.01,"toolUses":3}},
				{"agentId":"a-exec","role":"executor","usage":{"inputTokens":500,"outputTokens":100,"cachedTokens":0,"costUsd":0.005,"toolUses":1}}
			]}`,
		"thread.list": `[{"id":"th_1","title":"fix"}]`,
		"thread.get": `{"id":"th_1","exists":true,"title":"fix","agentOrder":["a-orch","a-exec"],
			"agents":{
				"a-orch":{"id":"a-orch","role":"orchestrator","model":"claude-opus-4-5"},
				"a-exec":{"id":"a-exec","role":"executor","model":"gpt-5-codex"}
			},"usage":{}}`,
	}}
	if err := renderUsage(context.Background(), env, fake, usageOptions{}); err != nil {
		t.Fatalf("renderUsage: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"AGENT", "ROLE", "MODEL", "INPUT", "OUTPUT", "CACHE", "COST",
		"a-orch", "orchestrator", "claude-opus-4-5",
		"a-exec", "executor", "gpt-5-codex",
		"1.0k", "500", "1.5k",
		"TOTAL  INPUT 1.5k  OUTPUT 300  CACHE 50  COST $0.0150",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestUsageThreadRendersUsageAndTurns(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{
		"thread.list": `[{"id":"th_9f2a1c7d","title":"nightly audit"},{"id":"th_41b0de22","title":"other job"}]`,
		"thread.get": `{
			"id":"th_9f2a1c7d","exists":true,"title":"nightly audit","turn":2,
			"agentOrder":["a-orch","a-exec"],
			"agents":{
				"a-orch":{"id":"a-orch","role":"orchestrator","model":"claude-opus-4-5"},
				"a-exec":{"id":"a-exec","role":"executor","model":"gpt-5-codex"}
			},
			"usage":{
				"a-orch":{"agentId":"a-orch","role":"orchestrator","usage":{"inputTokens":1000,"outputTokens":200,"costUsd":0.01}},
				"a-exec":{"agentId":"a-exec","role":"executor","usage":{"inputTokens":500,"outputTokens":100,"costUsd":0.005}}
			},
			"turns":[
				{"turn":1,"agents":{"a-orch":{"inputTokens":600,"outputTokens":100,"costUsd":0.006}}},
				{"turn":2,"agents":{"a-orch":{"inputTokens":400,"outputTokens":100,"costUsd":0.004},"a-exec":{"inputTokens":500,"outputTokens":100,"costUsd":0.005}}}
			]}`,
	}}
	if err := renderUsage(context.Background(), env, fake, usageOptions{thread: "th_9f2a"}); err != nil {
		t.Fatalf("renderUsage: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		`THREAD th_9f2a1c7d  "nightly audit"  turn #2`,
		"claude-opus-4-5", "gpt-5-codex",
		"TURN", "INPUT", "OUTPUT", "COST",
		"$0.0150",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	// The turn-1 row (600 input) precedes turn 2 (900 input, both agents).
	first, second := strings.Index(got, "600"), strings.Index(got, "900")
	if second < 0 || first < 0 || second < first {
		t.Fatalf("turn rows missing or out of order:\n%s", got)
	}
}

func TestUsageThreadRefMatching(t *testing.T) {
	threads := []usageThreadRef{
		{ID: "th_abc", Title: "fix authentication"},
		{ID: "th_abcd", Title: "fix tests"},
		{ID: "th_zzz", Title: "unrelated"},
	}
	cases := []struct {
		ref  string
		want string
	}{
		{"th_abc", "th_abc"},     // exact id wins over the longer prefix
		{"th_zzz", "th_zzz"},     // exact id
		{"fix tests", "th_abcd"}, // exact title
		{"TH_ABC", ""},           // ids are case-sensitive: no match
	}
	for _, tc := range cases {
		got, err := matchThreadRef(threads, tc.ref)
		if tc.want == "" {
			if err == nil {
				t.Fatalf("matchThreadRef(%q) = %q, want an error", tc.ref, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("matchThreadRef(%q) = %q, %v; want %q", tc.ref, got, err, tc.want)
		}
	}

	// A substring that matches several titles is ambiguous and lists them.
	_, err := matchThreadRef(threads, "fix")
	if err == nil {
		t.Fatal("ambiguous ref should fail")
	}
	for _, want := range []string{"ambiguous", "th_abc", "th_abcd"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}

	// A unique prefix resolves.
	if got, err := matchThreadRef(threads, "th_z"); err != nil || got != "th_zzz" {
		t.Fatalf("unique prefix = %q, %v", got, err)
	}

	if _, err := matchThreadRef(threads, "nope"); err == nil || !strings.Contains(err.Error(), "no thread matches") {
		t.Fatalf("no-match error = %v", err)
	}
	if _, err := matchThreadRef(threads, "  "); err == nil {
		t.Fatal("empty ref should be a usage error")
	} else {
		var ee exitError
		if !errors.As(err, &ee) || ee.code != 2 {
			t.Fatalf("empty ref exit code = %v, want usage error", err)
		}
	}
}

func TestUsageLimitsUnmetered(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{
		"usage.get":   zeroUsageJSON,
		"thread.list": `[]`,
		"usage.limits": `[
			{"harness":"claude","available":false,"reason":"no credentials","windows":[]},
			{"harness":"codex","available":true,"windows":[{"id":"five_hour","label":"5h","usedPercent":37,"resetsAt":"2026-11-11T18:00:00Z"}],"fetchedAt":"2026-11-11T12:00:00Z"},
			{"harness":"grok","available":true,"windows":[]}
		]`,
	}}
	if err := renderUsage(context.Background(), env, fake, usageOptions{limits: true}); err != nil {
		t.Fatalf("renderUsage: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "LIMITS") {
		t.Fatalf("missing limits section:\n%s", got)
	}
	if !strings.Contains(got, "unmetered") || !strings.Contains(got, "no credentials") {
		t.Fatalf("unavailable harness must render unmetered with its reason:\n%s", got)
	}
	if !strings.Contains(got, "37%") {
		t.Fatalf("metered window missing:\n%s", got)
	}
	if strings.Contains(got, "0%") {
		t.Fatalf("unmetered harness must never render 0%%:\n%s", got)
	}
	if strings.Count(got, "unmetered") != 2 {
		t.Fatalf("claude (unavailable) and grok (no windows) are both unmetered:\n%s", got)
	}
}

func TestUsageJSONPassthrough(t *testing.T) {
	threadJSON := `{"id":"th_1","exists":true,"title":"fix","agentOrder":[],"usage":{},"turns":[]}`
	ledger := `{"orchestrator":{"inputTokens":7},"subagents":{"inputTokens":0},"byAgent":[]}`

	t.Run("ledger is unchanged", func(t *testing.T) {
		env, out := newUsageTestEnv()
		env.JSON = true
		fake := &fakeCLICaller{results: map[string]string{"usage.get": ledger}}
		if err := renderUsage(context.Background(), env, fake, usageOptions{}); err != nil {
			t.Fatalf("renderUsage: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != ledger {
			t.Fatalf("json passthrough = %q, want %q", got, ledger)
		}
		if len(fake.calls) != 1 || fake.calls[0] != "usage.get" {
			t.Fatalf("calls = %v, want exactly usage.get", fake.calls)
		}
	})

	t.Run("thread is unchanged", func(t *testing.T) {
		env, out := newUsageTestEnv()
		env.JSON = true
		fake := &fakeCLICaller{results: map[string]string{
			"thread.list": `[{"id":"th_1","title":"fix"}]`,
			"thread.get":  threadJSON,
		}}
		if err := renderUsage(context.Background(), env, fake, usageOptions{thread: "th_1"}); err != nil {
			t.Fatalf("renderUsage: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != threadJSON {
			t.Fatalf("json passthrough = %q, want %q", got, threadJSON)
		}
	})

	t.Run("limits ride an envelope unchanged", func(t *testing.T) {
		env, out := newUsageTestEnv()
		env.JSON = true
		limits := `[{"harness":"claude","available":false,"reason":"none"}]`
		fake := &fakeCLICaller{results: map[string]string{
			"usage.get":    ledger,
			"usage.limits": limits,
		}}
		if err := renderUsage(context.Background(), env, fake, usageOptions{limits: true}); err != nil {
			t.Fatalf("renderUsage: %v", err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatalf("json envelope: %v\n%s", err, out.String())
		}
		if compactJSON(t, envelope["usage"]) != compactJSON(t, json.RawMessage(ledger)) ||
			compactJSON(t, envelope["limits"]) != compactJSON(t, json.RawMessage(limits)) {
			t.Fatalf("envelope = %s", out.String())
		}
	})
}

func TestUsageProjectScopesHarnessStats(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{
		"stats.harnesses": `[
			{"harness":"claude","threads":2,"agents":3,"subagents":2,"usage":{"inputTokens":2000,"outputTokens":400,"costUsd":0.02}},
			{"harness":"codex","threads":1,"agents":1,"subagents":0,"usage":{"inputTokens":100,"outputTokens":10,"costUsd":0.001}}
		]`,
	}}
	if err := renderUsage(context.Background(), env, fake, usageOptions{project: "p1"}); err != nil {
		t.Fatalf("renderUsage: %v", err)
	}
	got := out.String()
	for _, want := range []string{"PROJECT p1", "HARNESS", "THREADS", "claude", "codex", "2.1k", "$0.0210"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

// compactJSON normalizes indentation so two encodings of the same value compare.
func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buf.String()
}

func TestUsageRejectsThreadAndProjectTogether(t *testing.T) {
	if _, err := parseUsageArgs([]string{"--thread", "th_1", "--project", "p1"}); err == nil {
		t.Fatal("--thread with --project must be a usage error")
	}
	if _, err := parseUsageArgs([]string{"--thread"}); err == nil {
		t.Fatal("--thread without a value must be a usage error")
	}
}
