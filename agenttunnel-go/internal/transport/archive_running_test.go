package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// Archiving a thread with an unfinished agent is refused by the RPC with the
// same readable message delete gives, not the raw decider text.
func TestArchiveRefusesRunningThreadWithReadableMessage(t *testing.T) {
	ctx := context.Background()
	eng, runner, _ := newMessageFixture(t)
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Runner: runner})
	createThreadForTool(t, eng, "t1", nil)
	leaveAgentRunning(t, eng, "t1")

	params, _ := json.Marshal(map[string]any{"threadId": "t1", "enabled": true})
	_, err := disp.Handle(ctx, Request{ID: "1", Method: "thread.setArchived", Params: params})
	if !errors.Is(err, domain.ErrInvalidCommand) {
		t.Fatalf("archive of running thread: err = %v, want ErrInvalidCommand", err)
	}
	want := `invalid command: thread "t1" is running; stop it first`
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}

	// Restoring is never blocked by running work.
	params, _ = json.Marshal(map[string]any{"threadId": "t1", "enabled": false})
	if _, err := disp.Handle(ctx, Request{ID: "2", Method: "thread.setArchived", Params: params}); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
}
