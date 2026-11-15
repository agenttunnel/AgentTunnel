package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// PR writes must name their thread. Without one, threadWorkdir would fall back
// to the daemon's default checkout, so a missing threadId has to fail first.
func TestPRActionsRequireThreadID(t *testing.T) {
	_, disp := newCommandFixture(t)
	for _, method := range []string{"pr.commit", "pr.push", "pr.pull", "pr.create", "pr.ready", "pr.merge"} {
		params, _ := json.Marshal(map[string]any{"message": "fix", "all": true})
		_, err := disp.Handle(context.Background(), Request{Method: method, Params: params})
		if err == nil || !strings.Contains(err.Error(), "threadId is required") {
			t.Fatalf("%s without threadId: err = %v, want threadId refusal", method, err)
		}
	}
}
