package transport

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
)

// masterAndTurnScript answers the Nth home-screen call with replies[N-1]. Any
// other prompt is a thread turn: it parks until releaseFile exists, so a test
// can act on a turn that is still running.
func masterAndTurnScript(dir, releaseFile string, replies ...string) string {
	counter := filepath.Join(dir, "count")
	var b strings.Builder
	fmt.Fprintf(&b, `case "$0" in
*"home screen"*)
  n=$(cat %[1]q 2>/dev/null || echo 0)
  n=$((n+1))
  printf '%%s' "$n" > %[1]q
  case "$n" in
`, counter)
	for i, reply := range replies {
		frame, _ := json.Marshal(map[string]string{"type": "text", "text": reply})
		fmt.Fprintf(&b, "  %d)\n  cat <<'MASTER_OUT'\n%s\nMASTER_OUT\n  ;;\n", i+1, frame)
	}
	fmt.Fprintf(&b, `  esac
  ;;
*)
  while [ ! -f %q ]; do sleep 0.01; done
  printf '%%s\n' '{"type":"text","text":"turn finished"}'
  ;;
esac
`, releaseFile)
	return b.String()
}

// seedThreadForMaster creates an empty thread in project p1.
func seedThreadForMaster(t *testing.T, eng *domain.Engine, id contracts.ThreadID, title string) {
	t.Helper()
	if err := eng.Dispatch(context.Background(), contracts.Command{
		ID: "seed-" + string(id), Type: contracts.CmdThreadCreate, ThreadID: id, Title: title, ProjectID: "p1",
	}); err != nil {
		t.Fatal(err)
	}
}

func actionReply(actions string) string {
	return "Doing it.\n\n```json\n{\"actions\":[" + actions + "]}\n```"
}

func TestMasterActionsRenameSetMailboxAndReportStopWithoutTurn(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := actionReply(`{"type":"rename_thread","threadId":"th-a","title":"Renamed"},` +
		`{"type":"set_peer_mailbox","threadId":"th-a","enabled":true},` +
		`{"type":"stop_thread","threadId":"th-a"}`)
	eng, disp := newMasterRPCFixture(t, masterDispatchScript(promptFile, "", reply))
	seedThreadForMaster(t, eng, "th-a", "Old name")

	res := callMasterSend(t, disp, "tidy th-a")

	if len(res.Results) != 3 {
		t.Fatalf("Results = %+v, want one per action", res.Results)
	}
	if !res.Results[0].OK || !res.Results[1].OK {
		t.Fatalf("rename and mailbox should succeed: %+v", res.Results)
	}
	if res.Results[2].OK || !strings.Contains(res.Results[2].Error, "no turn is running") {
		t.Fatalf("stop with no turn = %+v, want a not-running error", res.Results[2])
	}
	if !strings.Contains(res.Error, "no turn is running") {
		t.Fatalf("Error = %q, want the failed action reported on the reply", res.Error)
	}
	st, err := eng.State(context.Background(), "th-a")
	if err != nil {
		t.Fatal(err)
	}
	if st.Title != "Renamed" || !st.PeerMailbox {
		t.Fatalf("thread = title %q peerMailbox %v, want the rename and mailbox applied", st.Title, st.PeerMailbox)
	}
}

func TestMasterActionsDeleteOnlyValidatedTargets(t *testing.T) {
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	reply := actionReply(`{"type":"delete_thread","threadId":"th-a"},` +
		`{"type":"delete_thread","threadId":"th-b","confirm":true}`)
	eng, disp := newMasterRPCFixture(t, masterDispatchScript(promptFile, "", reply))
	seedThreadForMaster(t, eng, "th-a", "Keep me")
	seedThreadForMaster(t, eng, "th-b", "Delete me")

	res := callMasterSend(t, disp, "delete th-b, I am sure")

	// th-a had no confirm, so validation rejected it before anything ran.
	if len(res.Results) != 2 || res.Results[0].OK || !res.Results[1].OK {
		t.Fatalf("Results = %+v, want th-a rejected and th-b deleted", res.Results)
	}
	a, _ := eng.State(context.Background(), "th-a")
	b, _ := eng.State(context.Background(), "th-b")
	if !a.Exists || a.Deleted {
		t.Fatalf("th-a must survive an unconfirmed delete: %+v", a)
	}
	if b.Exists {
		t.Fatalf("th-b should be deleted: %+v", b)
	}
}

func TestMasterActionSendMessageStartsThenSteersThenStops(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	// A parked turn must not outlive the test, whatever the assertions did.
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o644) })
	script := masterAndTurnScript(dir, release,
		actionReply(`{"type":"send_message","threadId":"th-a","prompt":"keep going"}`),
		actionReply(`{"type":"send_message","threadId":"th-a","prompt":"also check the tests"}`),
		actionReply(`{"type":"stop_thread","threadId":"th-a"}`),
	)
	eng, disp := newMasterRPCFixture(t, script)
	seedThreadForMaster(t, eng, "th-a", "Long run")

	started := callMasterSend(t, disp, "start on th-a")
	if len(started.Results) != 1 || !started.Results[0].OK {
		t.Fatalf("first send = %+v, want a started turn", started.Results)
	}
	waitThreadState(t, eng, "th-a", func(st *domain.ThreadState) bool {
		return st.Agents[orchestratorAgentID()].Status == contracts.AgentRunning
	})

	steered := callMasterSend(t, disp, "also check the tests")
	if len(steered.Results) != 1 || !steered.Results[0].OK {
		t.Fatalf("second send = %+v, want a steer", steered.Results)
	}
	st, err := eng.State(context.Background(), "th-a")
	if err != nil {
		t.Fatal(err)
	}
	if !hasSteering(st, "also check the tests") {
		t.Fatalf("steering message missing from transcript: %+v", st.Messages)
	}

	stopped := callMasterSend(t, disp, "stop th-a")
	if len(stopped.Results) != 1 || !stopped.Results[0].OK {
		t.Fatalf("stop = %+v, want the running turn stopped", stopped.Results)
	}
	waitThreadState(t, eng, "th-a", func(st *domain.ThreadState) bool {
		return st.Agents[orchestratorAgentID()].Status.Terminal()
	})
}

func hasSteering(st *domain.ThreadState, text string) bool {
	for _, m := range st.Messages {
		if m.Author == "steering" && m.Text == text {
			return true
		}
	}
	return false
}
