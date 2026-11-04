package daemon

import (
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestProjectsAndThreadsRoundTrip(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	type project struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Path    string `json:"path"`
		Threads int    `json:"threads"`
	}
	var first struct {
		Project  project   `json:"project"`
		Projects []project `json:"projects"`
	}
	json.Unmarshal(call(t, c, "1", "project.create", map[string]any{
		"name": "Alpha", "path": "/tmp/alpha",
	}), &first)
	projects := first.Projects
	if len(projects) != 1 || first.Project.Name != "Alpha" || first.Project.Path != "/tmp/alpha" {
		t.Fatalf("projects = %+v", first)
	}
	pid := first.Project.ID
	if pid == "" {
		t.Fatal("project id is empty")
	}

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "2", "thread.create", map[string]any{
		"title": "T1", "projectId": pid,
	}), &created)

	var threads []struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
	}
	json.Unmarshal(call(t, c, "3", "thread.list", nil), &threads)
	if len(threads) != 1 || threads[0].ProjectID != pid {
		t.Fatalf("threads = %+v", threads)
	}

	// The project list now counts that thread.
	json.Unmarshal(call(t, c, "4", "project.list", nil), &projects)
	if projects[0].Threads != 1 {
		t.Fatalf("thread count = %d, want 1", projects[0].Threads)
	}

	// Deleting the project drops it from the list.
	json.Unmarshal(call(t, c, "5", "project.delete", map[string]any{"id": pid}), &projects)
	if len(projects) != 0 {
		t.Fatalf("projects after delete = %+v", projects)
	}
}

func TestProjectCreateRequiresName(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)

	// call() fails the test on an RPC error, so issue a raw request and read the
	// error response directly.
	if _, errResp := rawCall(t, c, "1", "project.create", map[string]any{"path": "/tmp/x"}); errResp == nil {
		t.Fatal("expected an error when the project name is missing")
	}
}
