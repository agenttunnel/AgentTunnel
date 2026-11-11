package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const harnessListJSON = `{
	"all":["claude","codex"],
	"available":["claude"],
	"enabled":["claude"],
	"models":{"claude":["opus","sonnet"],"codex":["gpt-5"]}
}`

const settingsRolesJSON = `{"roles":[
	{"id":"orchestrator","name":"Orchestrator","harness":"claude","model":"opus","variant":"high","enabled":true},
	{"id":"executor","name":"Executor","harness":"codex","model":"gpt-5","variant":"","enabled":false}
]}`

func TestModelsListRendersVariantsAndDefaults(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{
		"harness.list": harnessListJSON,
		"settings.get": settingsRolesJSON,
	}}
	if err := renderModels(context.Background(), env, fake, ""); err != nil {
		t.Fatalf("renderModels: %v", err)
	}
	got := out.String()
	for _, want := range []string{"HARNESS", "MODEL", "VARIANT", "DEFAULT", "claude", "codex"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	// opus is the orchestrator's enabled default and carries variant "high".
	opus := parityLineContaining(got, "opus")
	if opus == "" || !strings.Contains(opus, "high") || !strings.Contains(opus, "default") {
		t.Fatalf("opus row = %q (output:\n%s)", opus, got)
	}
	// sonnet is discovered but not any role's default.
	sonnet := parityLineContaining(got, "sonnet")
	if sonnet == "" || strings.Contains(sonnet, "default") {
		t.Fatalf("sonnet row = %q (output:\n%s)", sonnet, got)
	}
}

func TestModelsListFiltersHarness(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{
		"harness.list": harnessListJSON,
		"settings.get": settingsRolesJSON,
	}}
	if err := renderModels(context.Background(), env, fake, "codex"); err != nil {
		t.Fatalf("renderModels: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "gpt-5") || strings.Contains(got, "opus") || strings.Contains(got, "sonnet") {
		t.Fatalf("filter leaked other harnesses:\n%s", got)
	}

	// A harness with no discovered models still lists, rather than vanishing.
	fake.results["harness.list"] = `{"all":["claude","grok"],"available":[],"enabled":[],"models":{"claude":["opus"]}}`
	out.Reset()
	if err := renderModels(context.Background(), env, fake, "grok"); err != nil {
		t.Fatalf("renderModels grok: %v", err)
	}
	if got := strings.TrimSpace(out.String()); !strings.Contains(got, "grok") {
		t.Fatalf("empty harness row missing:\n%s", got)
	}
}

func TestModelsListUnknownHarnessIsUsageError(t *testing.T) {
	env, _ := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{"harness.list": harnessListJSON}}
	err := renderModels(context.Background(), env, fake, "nope")
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("unknown harness error = %v, want usage error", err)
	}
	if !strings.Contains(err.Error(), "known: claude, codex") {
		t.Fatalf("error should name the known harnesses: %v", err)
	}
}

func TestModelsJSONPassthrough(t *testing.T) {
	env, out := newUsageTestEnv()
	env.JSON = true
	fake := &fakeCLICaller{results: map[string]string{"harness.list": harnessListJSON}}
	if err := renderModels(context.Background(), env, fake, "claude"); err != nil {
		t.Fatalf("renderModels: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != strings.TrimSpace(harnessListJSON) {
		t.Fatalf("json passthrough = %q", got)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("json mode made extra calls: %v", fake.calls)
	}
}

func TestHarnessesList(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{"harness.list": harnessListJSON}}
	if err := renderHarnesses(context.Background(), env, fake); err != nil {
		t.Fatalf("renderHarnesses: %v", err)
	}
	got := out.String()
	for _, want := range []string{"HARNESS", "AVAILABLE", "ENABLED", "MODELS", "yes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	claude := parityLineContaining(got, "claude")
	if claude == "" || !strings.Contains(claude, "yes") {
		t.Fatalf("claude row = %q", claude)
	}
	codex := parityLineContaining(got, "codex")
	if codex == "" || strings.Count(codex, "no") != 2 || !strings.Contains(codex, "1") {
		t.Fatalf("codex row = %q (want available=no enabled=no models=1)", codex)
	}
}

func TestHarnessesUnknownStateWhenListsAreNull(t *testing.T) {
	env, out := newUsageTestEnv()
	fake := &fakeCLICaller{results: map[string]string{
		"harness.list": `{"all":["claude"],"available":null,"enabled":null,"models":null}`,
	}}
	if err := renderHarnesses(context.Background(), env, fake); err != nil {
		t.Fatalf("renderHarnesses: %v", err)
	}
	row := parityLineContaining(out.String(), "claude")
	// available, enabled, and the model count are all unknown, never "no"/"0".
	if row == "" || strings.Contains(row, "no") || strings.Contains(row, "0") {
		t.Fatalf("row = %q", row)
	}
}

func TestHarnessJSONPassthrough(t *testing.T) {
	env, out := newUsageTestEnv()
	env.JSON = true
	fake := &fakeCLICaller{results: map[string]string{"harness.list": harnessListJSON}}
	if err := renderHarnesses(context.Background(), env, fake); err != nil {
		t.Fatalf("renderHarnesses: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != strings.TrimSpace(harnessListJSON) {
		t.Fatalf("json passthrough = %q", got)
	}
}

func TestHarnessRejectsArguments(t *testing.T) {
	var env Env
	var out strings.Builder
	env.Out, env.Err = &out, &out
	if err := runHarnessLs(context.Background(), &env, []string{"extra"}); err == nil {
		t.Fatal("extra argument must be a usage error")
	}
}

// lineContaining returns the first output line that contains needle.
func parityLineContaining(out, needle string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// The fake must keep satisfying cliCaller after the package's interface evolves.
var _ cliCaller = (*fakeCLICaller)(nil)

func TestUsageJSONModeDoesNotDecodeTypedResult(t *testing.T) {
	// A raw payload that would not fit the typed structs still passes through.
	env, out := newUsageTestEnv()
	env.JSON = true
	fake := &fakeCLICaller{results: map[string]string{"usage.get": `{"unexpected":"shape"}`}}
	if err := renderUsage(context.Background(), env, fake, usageOptions{}); err != nil {
		t.Fatalf("renderUsage: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["unexpected"] != "shape" {
		t.Fatalf("out = %q (%v)", out.String(), err)
	}
}
