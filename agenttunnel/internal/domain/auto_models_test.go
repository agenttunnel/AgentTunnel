package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestDecideAutoApprovedModelsTrimsAndDedupes(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	models := []string{" sonnet ", "sonnet", "", "opus"}
	events, err := Decide(contracts.Command{
		Type: contracts.CmdAutoModelsSet, ThreadID: "t1", AutoModels: &models,
	}, s)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := events[0].(contracts.AutoApprovedModelsSet)
	if !ok {
		t.Fatalf("event = %T", events[0])
	}
	if len(got.Models) != 2 || got.Models[0] != "sonnet" || got.Models[1] != "opus" {
		t.Fatalf("models = %v, want [sonnet opus]", got.Models)
	}
}

func TestDecideAutoApprovedModelsRequiresPayloadAndThread(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	if _, err := Decide(contracts.Command{
		Type: contracts.CmdAutoModelsSet, ThreadID: "t1",
	}, s); err == nil {
		t.Fatal("a missing payload should be rejected")
	}

	models := []string{"m"}
	missing := NewThreadState("t2")
	if _, err := Decide(contracts.Command{
		Type: contracts.CmdAutoModelsSet, ThreadID: "t2", AutoModels: &models,
	}, missing); err != ErrThreadNotFound {
		t.Fatalf("err = %v, want ErrThreadNotFound", err)
	}
}

func TestProjectAutoApprovedModelsSetAndClear(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	models := []string{"a", "b"}
	if err := Project(s, contracts.Event{Payload: contracts.AutoApprovedModelsSet{Models: models}}); err != nil {
		t.Fatal(err)
	}
	if len(s.AutoApprovedModels) != 2 || s.AutoApprovedModels[0] != "a" {
		t.Fatalf("models = %v, want [a b]", s.AutoApprovedModels)
	}
	// The projector copies the slice, so mutating the event payload later does
	// not change the projected state.
	models[0] = "changed"
	if s.AutoApprovedModels[0] != "a" {
		t.Fatalf("projected slice aliases the event: %v", s.AutoApprovedModels)
	}

	if err := Project(s, contracts.Event{Payload: contracts.AutoApprovedModelsSet{}}); err != nil {
		t.Fatal(err)
	}
	if len(s.AutoApprovedModels) != 0 {
		t.Fatalf("clearing should leave no models: %v", s.AutoApprovedModels)
	}
}
