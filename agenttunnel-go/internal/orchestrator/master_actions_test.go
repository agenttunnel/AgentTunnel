package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
)

// seedMasterThread creates an empty thread in a project.
func seedMasterThread(t *testing.T, eng *domain.Engine, ctx context.Context, id contracts.ThreadID, project string) {
	t.Helper()
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "seed-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id,
		Title: "Thread " + string(id), ProjectID: project,
	}); err != nil {
		t.Fatal(err)
	}
}

// masterSequenceScript answers the Nth master call with replies[N-1] and writes
// each call's prompt to dir/prompt-N, so a test can follow a read round.
func masterSequenceScript(dir string, replies ...string) string {
	counter := filepath.Join(dir, "count")
	var b strings.Builder
	fmt.Fprintf(&b, `n=$(cat %[1]q 2>/dev/null || echo 0)
n=$((n+1))
printf '%%s' "$n" > %[1]q
printf '%%s' "$0" > "%[2]s/prompt-$n"
case "$n" in
`, counter, dir)
	for i, reply := range replies {
		frame, _ := json.Marshal(map[string]string{"type": "text", "text": reply})
		fmt.Fprintf(&b, "%d)\ncat <<'MASTER_OUT'\n%s\nMASTER_OUT\n;;\n", i+1, frame)
	}
	b.WriteString("esac\n")
	return b.String()
}

func TestMasterParsesMultipleActionsAndKeepsProse(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := "Doing three things.\n\n```json\n" + `{"actions":[` +
		`{"type":"start_thread","projectId":"p1","title":"New work","prompt":"begin"},` +
		`{"type":"send_message","threadId":"th-a","prompt":"status?"},` +
		`{"type":"rename_thread","threadId":"th-a","title":"Renamed"}` +
		"]}\n```"
	_, m, eng, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	seedMasterThread(t, eng, ctx, "th-a", "p1")

	res := sendMaster(t, m, ctx, "do the three things")

	if res.Reply != "Doing three things." {
		t.Fatalf("Reply = %q, want the prose without the JSON", res.Reply)
	}
	if len(res.Actions) != 3 {
		t.Fatalf("Actions = %+v, want 3", res.Actions)
	}
	wantTypes := []string{"start_thread", "send_message", "rename_thread"}
	for i, want := range wantTypes {
		if res.Actions[i].Type != want {
			t.Fatalf("Actions[%d].Type = %q, want %q", i, res.Actions[i].Type, want)
		}
	}
	if res.Action == nil || res.Action.Type != "start_thread" {
		t.Fatalf("Action = %+v, want the first action for single-action clients", res.Action)
	}
	if res.Actions[2].ProjectID != "p1" {
		t.Fatalf("rename ProjectID = %q, want the thread's project filled from state", res.Actions[2].ProjectID)
	}
	if len(res.Results) != 0 {
		t.Fatalf("Results = %+v, want no rejections", res.Results)
	}
}

func TestMasterRejectsBadIDsAndKeepsValidActions(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"actions":[` +
		`{"type":"stop_thread","threadId":"th-missing"},` +
		`{"type":"send_message","threadId":"th-a","prompt":"keep going"},` +
		`{"type":"start_thread","projectId":"nope","title":"x","prompt":"y"},` +
		`{"type":"delete_thread","threadId":"th-a"}` +
		`]`)
	_, m, eng, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	seedMasterThread(t, eng, ctx, "th-a", "p1")

	res := sendMaster(t, m, ctx, "stop, continue, and delete")

	if len(res.Actions) != 1 || res.Actions[0].Type != "send_message" || res.Actions[0].ThreadID != "th-a" {
		t.Fatalf("Actions = %+v, want only the valid send_message", res.Actions)
	}
	if len(res.Results) != 3 {
		t.Fatalf("Results = %+v, want three rejections", res.Results)
	}
	for _, want := range []string{"does not exist", "not configured", "explicit confirmation"} {
		if !strings.Contains(res.Error, want) {
			t.Fatalf("Error = %q, want it to mention %q", res.Error, want)
		}
	}
	for _, r := range res.Results {
		if r.OK || r.Error == "" {
			t.Fatalf("rejection %+v must not be OK", r)
		}
	}
}

func TestMasterCapsActionsPerReply(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	var items []string
	for i := 0; i < 6; i++ {
		items = append(items, fmt.Sprintf(`{"type":"rename_thread","threadId":"th-a","title":"Name %d"}`, i))
	}
	_, m, eng, ctx := newMasterHarness(t, masterScript(promptFile, masterActionReply(`"actions":[`+strings.Join(items, ",")+`]`)))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	seedMasterThread(t, eng, ctx, "th-a", "p1")

	res := sendMaster(t, m, ctx, "rename a lot")

	if len(res.Actions) != masterMaxActions {
		t.Fatalf("len(Actions) = %d, want %d", len(res.Actions), masterMaxActions)
	}
	if len(res.Results) != 1 || !strings.Contains(res.Results[0].Error, "at most 5") {
		t.Fatalf("Results = %+v, want one over-cap rejection", res.Results)
	}
}

func TestMasterDeleteRequiresConfirm(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	_, m, eng, ctx := newMasterHarness(t, masterScript(promptFile,
		masterActionReply(`"actions":[{"type":"delete_thread","threadId":"th-a","confirm":true}]`)))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	seedMasterThread(t, eng, ctx, "th-a", "p1")

	res := sendMaster(t, m, ctx, "delete th-a, I am sure")

	if len(res.Actions) != 1 || res.Actions[0].Type != "delete_thread" || !res.Actions[0].Confirm {
		t.Fatalf("Actions = %+v, want a confirmed delete_thread", res.Actions)
	}
	if res.Actions[0].ProjectID != "p1" {
		t.Fatalf("ProjectID = %q, want p1 from state", res.Actions[0].ProjectID)
	}
}

func TestMasterRejectsEmptyRenameMissingMailboxFlagAndUnknownType(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := masterActionReply(`"actions":[` +
		`{"type":"rename_thread","threadId":"th-a","title":"   "},` +
		`{"type":"set_peer_mailbox","threadId":"th-a"},` +
		`{"type":"fork_thread","threadId":"th-a"}` +
		`]`)
	_, m, eng, ctx := newMasterHarness(t, masterScript(promptFile, reply))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	seedMasterThread(t, eng, ctx, "th-a", "p1")

	res := sendMaster(t, m, ctx, "tweak it")

	if len(res.Actions) != 0 {
		t.Fatalf("Actions = %+v, want none", res.Actions)
	}
	for _, want := range []string{"needs a title", "needs enabled", "unsupported action"} {
		if !strings.Contains(res.Error, want) {
			t.Fatalf("Error = %q, want it to mention %q", res.Error, want)
		}
	}
}

func TestMasterRejectsDeletedThread(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	_, m, eng, ctx := newMasterHarness(t, masterScript(promptFile,
		masterActionReply(`"actions":[{"type":"send_message","threadId":"th-a","prompt":"hi"}]`)))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	seedMasterThread(t, eng, ctx, "th-a", "p1")
	if err := eng.Dispatch(ctx, contracts.Command{ID: "gone", Type: contracts.CmdThreadDelete, ThreadID: "th-a"}); err != nil {
		t.Fatal(err)
	}

	res := sendMaster(t, m, ctx, "hi")

	if len(res.Actions) != 0 || !strings.Contains(res.Error, "does not exist") {
		t.Fatalf("result = %+v, want the deleted thread rejected", res)
	}
}

func TestMasterReadThreadFeedsContentBackOnce(t *testing.T) {
	dir := t.TempDir()
	reads := masterActionReply(`"actions":[{"type":"read_thread","threadId":"th-a"}]`)
	acts := masterActionReply(`"actions":[{"type":"rename_thread","threadId":"th-a","title":"After reading"}]`)
	_, m, eng, ctx := newMasterHarness(t, masterSequenceScript(dir, reads, acts))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	seedMasterThread(t, eng, ctx, "th-a", "p1")
	if err := eng.Dispatch(ctx, contracts.Command{
		ID: "msg", Type: contracts.CmdMessageAppend, ThreadID: "th-a",
		Message: &contracts.MessageSent{MessageID: "m1", Author: "user", Text: "the linux build broke"},
	}); err != nil {
		t.Fatal(err)
	}

	res := sendMaster(t, m, ctx, "what happened in th-a?")

	first, _ := os.ReadFile(filepath.Join(dir, "prompt-1"))
	second, _ := os.ReadFile(filepath.Join(dir, "prompt-2"))
	if strings.Contains(string(first), "Requested thread content") {
		t.Fatal("first call already had requested content")
	}
	if !strings.Contains(string(second), "Requested thread content") || !strings.Contains(string(second), "the linux build broke") {
		t.Fatalf("second prompt lacks the read thread's messages")
	}
	if len(res.Actions) != 1 || res.Actions[0].Type != "rename_thread" {
		t.Fatalf("Actions = %+v, want the rename proposed after reading", res.Actions)
	}
	if len(res.Results) != 0 {
		t.Fatalf("Results = %+v, want none", res.Results)
	}
}

func TestMasterReadThreadRoundsAreCapped(t *testing.T) {
	dir := t.TempDir()
	reads := masterActionReply(`"actions":[{"type":"read_thread","threadId":"th-a"}]`)
	_, m, eng, ctx := newMasterHarness(t, masterSequenceScript(dir, reads, reads, reads, reads))
	m.settings.AddProject(settings.Project{ID: "p1", Name: "Proj", Path: t.TempDir()})
	seedMasterThread(t, eng, ctx, "th-a", "p1")

	res := sendMaster(t, m, ctx, "read it")

	// One initial call plus masterReadRounds re-asks, and no more.
	for n := 1; n <= masterReadRounds+1; n++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("prompt-%d", n))); err != nil {
			t.Fatalf("call %d did not run: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("prompt-%d", masterReadRounds+2))); err == nil {
		t.Fatalf("model was asked more than %d times", masterReadRounds+1)
	}
	if len(res.Results) != 1 || !strings.Contains(res.Results[0].Error, "read limit reached") {
		t.Fatalf("Results = %+v, want one read-limit rejection", res.Results)
	}
}

func TestMasterPromptStaysBoundedWithManyThreads(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	_, m, eng, ctx := newMasterHarness(t, masterScript(promptFile, "Nothing to do."))
	const projects, perProject = 60, 15
	for p := 0; p < projects; p++ {
		pid := fmt.Sprintf("p%02d", p)
		m.settings.AddProject(settings.Project{ID: pid, Name: "Project " + pid, Path: t.TempDir()})
		for i := 0; i < perProject; i++ {
			seedMasterThread(t, eng, ctx, contracts.ThreadID(fmt.Sprintf("th-%s-%02d", pid, i)), pid)
		}
	}

	sendMaster(t, m, ctx, "what is going on?")

	raw, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(raw)
	if len(prompt) > 40<<10 {
		t.Fatalf("prompt is %d bytes with %d threads, want a bounded snapshot", len(prompt), projects*perProject)
	}
	if !strings.Contains(prompt, "more threads omitted") {
		t.Fatal("prompt lists every thread; want the section budget to cut the compact rows")
	}
}

func TestMasterThreadDetailIsClipped(t *testing.T) {
	long := strings.Repeat("x", 5000)
	st := &domain.ThreadState{ID: "th-a", Exists: true, ProjectID: "p1", Title: "Big"}
	for i := 0; i < 10; i++ {
		st.Messages = append(st.Messages, domain.Message{ID: fmt.Sprint(i), Author: "user", Text: long})
	}
	detail := masterThreadDetail(st)
	if len(detail) > masterDetailBytes+len("…") {
		t.Fatalf("detail is %d bytes, want at most %d", len(detail), masterDetailBytes)
	}
	if !strings.Contains(detail, "recent:") {
		t.Fatalf("detail = %q, want recent messages", detail)
	}
}

func TestMasterThreadReadIsClippedToNewest(t *testing.T) {
	st := &domain.ThreadState{ID: "th-a", Exists: true, Title: "Big"}
	for i := 0; i < 50; i++ {
		st.Messages = append(st.Messages, domain.Message{ID: fmt.Sprint(i), Author: "user", Text: fmt.Sprintf("msg-%02d %s", i, strings.Repeat("y", 900))})
	}
	read := masterThreadRead("th-a", st)
	if len(read) > masterReadBytes+200 {
		t.Fatalf("read is %d bytes, want at most about %d", len(read), masterReadBytes)
	}
	if !strings.Contains(read, "msg-49") || strings.Contains(read, "msg-00") {
		t.Fatal("read should keep the newest messages, not the oldest")
	}
}
