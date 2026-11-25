package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A cancelled curator run must not outlive its cancel. The harness leaves a
// grandchild in its own session holding stdout open, so killing the harness's
// group does not close the pipe; the run must still return on cancel rather
// than wait for that grandchild to exit. Otherwise the turn that waits on the
// curator stays in working state after its orchestrator has finished.
func TestCancelledCuratorRunReturnsWhileHarnessPipeIsHeld(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	// The grandchild touches the marker only after setsid, so cancelling once
	// the marker exists lands with the grandchild already out of the group.
	script := fmt.Sprintf(`perl -MPOSIX -e 'setsid(); open my $f, ">", $ARGV[0]; close $f; sleep 30' %q & sleep 30`, started)
	r, _, ctx := newHarness(t, script)
	r.settings.SetExperimentalRoles(true)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	task := contracts.DispatchTask{AgentID: "agent-context-1", Name: ContextKey, Role: contracts.RoleContextManager, Task: contextCuratorTask}
	done := make(chan error, 1)
	go func() {
		_, err := r.runTask(runCtx, "t1", task, true)
		done <- err
	}()

	waitForFile(t, started, nil)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runTask error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled curator run kept waiting on the harness pipe")
	}
}
