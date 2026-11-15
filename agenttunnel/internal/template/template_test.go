package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	data := []byte(`---
name: nightly-audit
project: ~/code/acme
modelConfig:
  orchestrator: {harness: claude, model: claude-opus-4-5}
  executor: {harness: codex, model: gpt-5-codex, variant: high}
---
Audit this repository for dependency drift.
Open one PR per finding.
`)
	tmpl, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tmpl.Name != "nightly-audit" {
		t.Fatalf("name = %q", tmpl.Name)
	}
	if tmpl.Project != "~/code/acme" {
		t.Fatalf("project = %q", tmpl.Project)
	}
	want := "Audit this repository for dependency drift.\nOpen one PR per finding.\n"
	if tmpl.Body != want {
		t.Fatalf("body = %q, want %q", tmpl.Body, want)
	}
	exec, ok := tmpl.ModelConfig["executor"]
	if !ok {
		t.Fatalf("modelConfig = %+v", tmpl.ModelConfig)
	}
	if exec.Harness != "codex" || exec.Model != "gpt-5-codex" || exec.Variant != "high" {
		t.Fatalf("executor = %+v", exec)
	}
}

func TestParseDefaultsProject(t *testing.T) {
	tmpl, err := Parse([]byte("---\nname: x\n---\ndo it\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tmpl.Project != "." {
		t.Fatalf("project = %q, want .", tmpl.Project)
	}
}

func TestParseMissingBody(t *testing.T) {
	for _, data := range []string{
		"---\nname: x\n---\n",
		"---\nname: x\n---\n\n \n",
		"---\nname: x\n---",
	} {
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "body is empty") {
			t.Fatalf("Parse(%q) err = %v, want empty body", data, err)
		}
	}
}

func TestParseUnknownRole(t *testing.T) {
	data := []byte(`---
name: x
modelConfig:
  sre: {harness: codex}
---
do it
`)
	_, err := Parse(data)
	if err == nil || !strings.Contains(err.Error(), `unknown modelConfig role "sre"`) {
		t.Fatalf("err = %v, want unknown role", err)
	}
}

func TestParseUnknownKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		key  string
	}{
		{
			name: "top level",
			data: "---\nname: x\ntitel: nope\n---\ndo it\n",
			key:  "titel",
		},
		{
			name: "nested",
			data: "---\nname: x\nmodelConfig:\n  executor: {harnes: codex}\n---\ndo it\n",
			key:  "harnes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("err = %v, want a complaint about %q", err, tc.key)
			}
		})
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	_, err := Parse([]byte("# Just markdown\n\nno settings here\n"))
	if err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Fatalf("err = %v, want frontmatter error", err)
	}
}

func TestParseUnclosedFrontmatter(t *testing.T) {
	_, err := Parse([]byte("---\nname: x\nbody without close\n"))
	if err == nil || !strings.Contains(err.Error(), "not closed") {
		t.Fatalf("err = %v, want unclosed error", err)
	}
}

func TestParseRequiresName(t *testing.T) {
	_, err := Parse([]byte("---\nproject: .\n---\ndo it\n"))
	if err == nil || !strings.Contains(err.Error(), `"name" is required`) {
		t.Fatalf("err = %v, want required name", err)
	}
}

func TestParseCRLF(t *testing.T) {
	tmpl, err := Parse([]byte("---\r\nname: x\r\n---\r\ndo it\r\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tmpl.Name != "x" || !strings.Contains(tmpl.Body, "do it") {
		t.Fatalf("tmpl = %+v", tmpl)
	}
}

func TestLoadDefaultsNameFromFilename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightly-audit.md")
	if err := os.WriteFile(path, []byte("---\nproject: .\n---\ndo it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if tmpl.Name != "nightly-audit" {
		t.Fatalf("name = %q, want file stem", tmpl.Name)
	}
	if tmpl.Path != path {
		t.Fatalf("path = %q", tmpl.Path)
	}
}

func TestScaffoldRoundTrip(t *testing.T) {
	scaffold := Scaffold("nightly-audit")
	tmpl, err := Parse(scaffold)
	if err != nil {
		t.Fatalf("Parse(Scaffold()) failed: %v\n---\n%s", err, scaffold)
	}
	if tmpl.Name != "nightly-audit" {
		t.Fatalf("name = %q", tmpl.Name)
	}
	if tmpl.Project != "." {
		t.Fatalf("project = %q", tmpl.Project)
	}
	if strings.TrimSpace(tmpl.Body) == "" {
		t.Fatal("scaffold body is empty")
	}
	if _, ok := tmpl.ModelConfig["orchestrator"]; !ok {
		t.Fatalf("scaffold does not demonstrate orchestrator: %+v", tmpl.ModelConfig)
	}
	if _, ok := tmpl.ModelConfig["executor"]; !ok {
		t.Fatalf("scaffold does not demonstrate executor: %+v", tmpl.ModelConfig)
	}

	// The same bytes must survive a round trip through a file.
	dir := t.TempDir()
	path := filepath.Join(dir, "job.md")
	if err := os.WriteFile(path, scaffold, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(scaffold) failed: %v", err)
	}
	if loaded.Name != "nightly-audit" || loaded.Body != tmpl.Body {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestScaffoldDefaultName(t *testing.T) {
	tmpl, err := Parse(Scaffold(""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tmpl.Name != "agenttunnel" {
		t.Fatalf("name = %q, want agenttunnel", tmpl.Name)
	}
}

func TestFindPrecedence(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("---\nname: x\n---\ndo it\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	exact := write("job")
	withMD := write("job.md")
	withSuffix := write("job.agenttunnel.md")

	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "exact", ref: "job", want: exact},
		{name: "dot md", ref: "job", want: withMD},
		{name: "dot agenttunnel", ref: "job", want: withSuffix},
	} {
		got, err := Find(dir, tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		// Remove the winner so the next case proves the next candidate.
		if err := os.Remove(tc.want); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Find(dir, "job"); err == nil {
		t.Fatal("expected an error once every candidate is gone")
	}
}

func TestFindDefaultAndAbsolute(t *testing.T) {
	dir := t.TempDir()
	if _, err := Find(dir, ""); err == nil {
		t.Fatal("expected an error when no agenttunnel.md exists")
	}
	def := filepath.Join(dir, DefaultName)
	if err := os.WriteFile(def, []byte("---\nname: x\n---\ndo it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Find(dir, "")
	if err != nil || got != def {
		t.Fatalf("got %q, err %v", got, err)
	}
	got, err = Find(t.TempDir(), def)
	if err != nil || got != def {
		t.Fatalf("absolute ref: got %q, err %v", got, err)
	}
}
