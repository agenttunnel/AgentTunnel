package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jobtemplate "github.com/agenttunnel/agenttunnel-go/internal/template"
)

func TestTemplateCommandWritesScaffold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightly-audit.md")
	var out, errOut bytes.Buffer
	env := &Env{Out: &out, Err: &errOut}

	if err := runTemplate(context.Background(), env, []string{path}); err != nil {
		t.Fatalf("runTemplate: %v", err)
	}
	if !strings.Contains(out.String(), "wrote "+path) {
		t.Fatalf("output = %q, want the written path", out.String())
	}
	if !strings.Contains(out.String(), "edit it, then at run "+path) {
		t.Fatalf("output = %q, want the next step", out.String())
	}

	tmpl, err := jobtemplate.Load(path)
	if err != nil {
		t.Fatalf("the written scaffold does not parse: %v", err)
	}
	if tmpl.Name != "nightly-audit" {
		t.Fatalf("name = %q, want the file stem", tmpl.Name)
	}
	if strings.TrimSpace(tmpl.Body) == "" {
		t.Fatal("scaffold body is empty")
	}

	// A second run must not overwrite the user's edits.
	out.Reset()
	if err := runTemplate(context.Background(), env, []string{path}); err == nil {
		t.Fatal("expected a refusal to overwrite an existing template")
	} else if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a --force hint", err)
	}

	// --force replaces it again.
	out.Reset()
	if err := runTemplate(context.Background(), env, []string{"--force", path}); err != nil {
		t.Fatalf("runTemplate --force: %v", err)
	}
}

func TestTemplateCommandDefaultPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out bytes.Buffer
	env := &Env{Out: &out, Err: &out}

	if err := runTemplate(context.Background(), env, nil); err != nil {
		t.Fatalf("runTemplate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, jobtemplate.DefaultName)); err != nil {
		t.Fatalf("default template not written: %v", err)
	}
}

func TestTemplateCommandRejectsExtraArgs(t *testing.T) {
	env := &Env{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := runTemplate(context.Background(), env, []string{"a.md", "b.md"}); err == nil {
		t.Fatal("expected a usage error for two paths")
	}
}
