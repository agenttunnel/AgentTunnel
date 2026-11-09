package contracts

import (
	"encoding/json"
	"testing"
	"time"
)

// Every persisted payload must survive a marshal/unmarshal round trip, since the
// event log is replayed on start.
func TestEventPayloadsRoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	payloads := []EventPayload{
		ThreadCreated{Title: "my thread", ProjectID: "proj_1"},
		ThreadCreated{Title: "sub", ProjectID: "proj_1", ParentThreadID: "t1"},
		SubthreadSpawned{ThreadID: "t2"},
		ThreadRenamed{Title: "renamed thread"},
		MessageSent{MessageID: "m1", Author: "user", Text: "hello"},
		ModelSet{Selection: ModelSelection{InstanceID: "i", Model: "gpt-5"}},
		AutoModeSet{Enabled: true},
		PeerMailboxSet{Enabled: true},
		TaskDispatched{AgentID: "a1", ParentAgentID: "orch", Name: "explore", Role: RolePlanner, Task: "map it", Model: "m", PipelineID: "p1"},
		AgentStatusChanged{AgentID: "a1", Status: AgentRunning, Reason: "go"},
		UsageRecorded{AgentID: "a1", Role: RoleExecutor, Usage: TokenUsage{InputTokens: 5, OutputTokens: 6, CostUSD: 0.25}},
		SharedContextSet{Key: "k", Value: "v", AuthorID: "a1"},
		MailboxPosted{From: "a1", To: "a2", Kind: "note", Body: "hi"},
		ReviewCommentAdded{ID: "r1", File: "main.go", Line: 12, Body: "rename", AuthorID: "user", CreatedAt: at},
		TaskAdded{Name: "api", Title: "Design the API"},
		TaskStatusChanged{Name: "api", Status: TaskDoing},
	}

	for _, payload := range payloads {
		ev := Event{Seq: 3, ThreadID: "t1", At: at, Payload: payload}

		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("%T: marshal: %v", payload, err)
		}

		var back Event
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("%T: unmarshal: %v", payload, err)
		}

		if back.Type() != ev.Type() {
			t.Fatalf("%T: type = %s, want %s", payload, back.Type(), ev.Type())
		}
		if back.Seq != ev.Seq || back.ThreadID != ev.ThreadID {
			t.Fatalf("%T: envelope changed: %+v", payload, back)
		}
		// Re-marshalling must be byte-identical, proving the payload survived.
		again, err := json.Marshal(back)
		if err != nil {
			t.Fatalf("%T: re-marshal: %v", payload, err)
		}
		if string(again) != string(data) {
			t.Fatalf("%T: round trip changed the event\n a=%s\n b=%s", payload, data, again)
		}
	}
}

func TestUnmarshalPayloadRejectsUnknownType(t *testing.T) {
	if _, err := UnmarshalPayload("nope", []byte(`{}`)); err == nil {
		t.Fatal("expected an error for an unknown event type")
	}
}
