package orchestrator

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A structured tool call reaches both the persisted log and the live stream,
// with the call and its result sharing an id so clients can fold them.
func TestRunnerKeepsStructuredToolCalls(t *testing.T) {
	script := `printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}'
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a.txt"}]}}'
printf '%s\n' '{"type":"text","text":"done"}'`

	r, eng, ctx := newHarness(t, script)
	events, unsub := eng.Subscribe("t1")
	defer unsub()

	tasks := []contracts.DispatchTask{{AgentID: "agent-a", Name: "a", Role: contracts.RoleExecutor, Task: "list"}}
	if err := r.Dispatch(ctx, "t1", tasks); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := r.Run(ctx, "t1", tasks); err != nil {
		t.Fatalf("run: %v", err)
	}

	var logged []*contracts.ToolCall
	for _, e := range r.Logs().Entries("t1") {
		if e.Tool != nil {
			logged = append(logged, e.Tool)
		}
	}
	if len(logged) != 2 || logged[0].ID != "toolu_1" || logged[0].Status != contracts.ToolRunning ||
		logged[1].ID != "toolu_1" || logged[1].Status != contracts.ToolDone {
		t.Fatalf("logged tool calls = %+v", logged)
	}

	// Output is published before Run returns; drain what is buffered.
	var streamed []*contracts.ToolCall
	for {
		select {
		case ev := <-events:
			if out, ok := ev.Payload.(contracts.AgentOutput); ok && out.Tool != nil {
				streamed = append(streamed, out.Tool)
			}
			continue
		default:
		}
		break
	}
	if len(streamed) != 2 || streamed[0].Command != "ls" || streamed[1].Status != contracts.ToolDone {
		t.Fatalf("streamed tool calls = %+v", streamed)
	}
}
