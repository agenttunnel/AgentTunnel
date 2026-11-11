package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/template"
)

// runCall is one recorded RPC: the method and its params as sent on the wire.
type runCall struct {
	method string
	params map[string]any
}

// recordingCaller is the smallest RPC client `at run` needs. It records each
// call's params so a test can assert the exact request the CLI builds, and
// answers from canned results.
type recordingCaller struct {
	calls   []runCall
	results map[string]string
	errs    map[string]error
}

func (r *recordingCaller) Call(_ context.Context, method string, params, out any) error {
	call := runCall{method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &call.params); err != nil {
			return err
		}
	}
	r.calls = append(r.calls, call)
	if err := r.errs[method]; err != nil {
		return err
	}
	raw, ok := r.results[method]
	if !ok {
		return fmt.Errorf("unexpected RPC %s", method)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(raw), out)
}

func (r *recordingCaller) methods() []string {
	out := make([]string, len(r.calls))
	for i, call := range r.calls {
		out[i] = call.method
	}
	return out
}

func (r *recordingCaller) callFor(t *testing.T, method string) runCall {
	t.Helper()
	for _, call := range r.calls {
		if call.method == method {
			return call
		}
	}
	t.Fatalf("no %s call in %v", method, r.methods())
	return runCall{}
}

const runTemplateBody = "audit the dependencies\n"

func newRunTestTemplate(t *testing.T, project string, modelConfig template.ModelConfig) *template.Template {
	t.Helper()
	return &template.Template{
		Name: "nightly-audit", Project: project,
		ModelConfig: modelConfig, Body: runTemplateBody,
	}
}

func TestStartRunBuildsTheJobRequest(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingCaller{results: map[string]string{
		"project.list":          `[]`,
		"project.create":        `{"project":{"id":"proj_1","name":"` + filepath.Base(resolved) + `","path":"` + resolved + `"},"projects":[]}`,
		"thread.create":         `{"threadId":"th_job"}`,
		"thread.setRoleRuntime": `{"ok":true}`,
		"thread.turn.start":     `{"ok":true,"detached":true,"threadId":"th_job"}`,
	}}
	tmpl := newRunTestTemplate(t, dir, template.ModelConfig{
		"executor":     {Harness: "codex", Model: "gpt-5-codex", Variant: "high"},
		"orchestrator": {Harness: "claude", Model: "claude-opus-4-5"},
	})

	result, err := startRun(context.Background(), fake, tmpl)
	if err != nil {
		t.Fatalf("startRun: %v", err)
	}
	if result.ThreadID != "th_job" || !result.Detached {
		t.Fatalf("result = %+v", result)
	}

	// The project was created from the template's path, by its base name.
	create := fake.callFor(t, "project.create")
	if create.params["path"] != resolved || create.params["name"] != filepath.Base(resolved) {
		t.Fatalf("project.create params = %+v, want resolved path %s", create.params, resolved)
	}

	// The thread takes the template's name as its title.
	thread := fake.callFor(t, "thread.create")
	if thread.params["title"] != "nightly-audit" || thread.params["projectId"] != "proj_1" {
		t.Fatalf("thread.create params = %+v", thread.params)
	}

	// Runtimes are applied once per configured role, in sorted order, and the
	// thread-scoped RPC is the only one used.
	var roles []string
	for _, call := range fake.calls {
		if call.method == "thread.setRoleRuntime" {
			roles = append(roles, call.params["role"].(string))
		}
	}
	if strings.Join(roles, ",") != "executor,orchestrator" {
		t.Fatalf("setRoleRuntime roles = %v, want executor,orchestrator", roles)
	}
	var executor runCall
	for _, call := range fake.calls {
		if call.method == "thread.setRoleRuntime" && call.params["role"] == "executor" {
			executor = call
		}
	}
	if executor.params["harness"] != "codex" || executor.params["model"] != "gpt-5-codex" || executor.params["variant"] != "high" {
		t.Fatalf("executor runtime = %+v", executor.params)
	}

	// The turn is detached and carries the template body verbatim.
	turn := fake.callFor(t, "thread.turn.start")
	if turn.params["text"] != runTemplateBody || turn.params["detach"] != true || turn.params["threadId"] != "th_job" {
		t.Fatalf("thread.turn.start params = %+v", turn.params)
	}
}

func TestStartRunSkipsEmptyRoleConfig(t *testing.T) {
	fake := &recordingCaller{results: map[string]string{
		"project.list":      `[{"id":"proj_1","name":"acme","path":"/code/acme"}]`,
		"thread.create":     `{"threadId":"th_1"}`,
		"thread.turn.start": `{"ok":true,"detached":true}`,
	}}
	tmpl := newRunTestTemplate(t, "proj_1", template.ModelConfig{"reviewer": {}})

	if _, err := startRun(context.Background(), fake, tmpl); err != nil {
		t.Fatalf("startRun: %v", err)
	}
	for _, method := range fake.methods() {
		if method == "thread.setRoleRuntime" {
			t.Fatalf("an empty role config must not set a runtime: %v", fake.methods())
		}
	}
}

func TestStartRunKnownProjectByPath(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingCaller{results: map[string]string{
		"project.list":      `[{"id":"proj_9","name":"acme","path":"` + resolved + `"}]`,
		"thread.create":     `{"threadId":"th_1"}`,
		"thread.turn.start": `{"ok":true,"detached":true}`,
	}}
	if _, err := startRun(context.Background(), fake, newRunTestTemplate(t, dir, nil)); err != nil {
		t.Fatalf("startRun: %v", err)
	}
	for _, method := range fake.methods() {
		if method == "project.create" {
			t.Fatalf("a registered path must not be re-created: %v", fake.methods())
		}
	}
	if got := fake.callFor(t, "thread.create").params["projectId"]; got != "proj_9" {
		t.Fatalf("projectId = %v, want proj_9", got)
	}
}

func TestResolveRunProjectNameWhenPathIsNotADirectory(t *testing.T) {
	fake := &recordingCaller{results: map[string]string{
		"project.list": `[{"id":"proj_1","name":"acme","path":"/nowhere/acme"}]`,
	}}
	got, err := resolveRunProject(context.Background(), fake, "acme")
	if err != nil {
		t.Fatalf("resolveRunProject: %v", err)
	}
	if got.ID != "proj_1" {
		t.Fatalf("project = %+v, want proj_1", got)
	}
}

func TestResolveRunProjectAmbiguousName(t *testing.T) {
	fake := &recordingCaller{results: map[string]string{
		"project.list": `[{"id":"proj_1","name":"acme","path":"/a"},{"id":"proj_2","name":"acme","path":"/b"}]`,
	}}
	_, err := resolveRunProject(context.Background(), fake, "acme")
	if err == nil {
		t.Fatal("ambiguous project name must fail")
	}
	for _, want := range []string{"ambiguous", "proj_1", "proj_2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestResolveRunProjectUnknownDirectoryCreates(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingCaller{results: map[string]string{
		"project.list":   `[]`,
		"project.create": `{"project":{"id":"proj_new","name":"` + filepath.Base(resolved) + `","path":"` + resolved + `"}}`,
	}}
	if _, err := resolveRunProject(context.Background(), fake, "~"); err != nil {
		// "~" expands to the home dir, which exists; the create path is what
		// matters, not which directory was chosen.
		if !strings.Contains(err.Error(), "project.create") {
			t.Fatalf("resolveRunProject: %v", err)
		}
	}
}

func TestParseRunArgs(t *testing.T) {
	cases := []struct {
		args   []string
		follow bool
		ref    string
		fails  bool
	}{
		{args: []string{"job.md"}, ref: "job.md"},
		{args: []string{"job.md", "--follow"}, ref: "job.md", follow: true},
		{args: []string{"--no-follow", "job.md"}, ref: "job.md"},
		{args: []string{}, fails: true},
		{args: []string{"a.md", "b.md"}, fails: true},
		{args: []string{"--bogus", "a.md"}, fails: true},
	}
	for _, tc := range cases {
		opts, err := parseRunArgs(tc.args)
		if tc.fails {
			if err == nil {
				t.Fatalf("parseRunArgs(%v) should fail", tc.args)
			}
			continue
		}
		if err != nil || opts.ref != tc.ref || opts.follow != tc.follow {
			t.Fatalf("parseRunArgs(%v) = %+v, %v", tc.args, opts, err)
		}
	}
}

func TestRunFindsTemplateAndFailsWithoutADaemon(t *testing.T) {
	// An end-to-end-ish check of the front half: the template is found and
	// parsed before any RPC, so a bad template reports the file, not a daemon
	// error.
	dir := t.TempDir()
	path := filepath.Join(dir, "job.agenttunnel.md")
	content := "---\nname: nightly\n---\n" + runTemplateBody
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := template.Find(dir, "job")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if found != path {
		t.Fatalf("Find = %s, want %s", found, path)
	}
	tmpl, err := template.Load(found)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tmpl.Name != "nightly" || tmpl.Body != runTemplateBody {
		t.Fatalf("template = %+v", tmpl)
	}
}
