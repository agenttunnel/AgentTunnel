package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestDecideAutoApprovedModelsTrimsAndDedupes(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	models := map[string][]string{"claude": {" sonnet ", "sonnet", "", "opus"}, "": {"dropped"}}
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
	if len(got.Models) != 1 || len(got.Models["claude"]) != 2 || got.Models["claude"][0] != "sonnet" || got.Models["claude"][1] != "opus" {
		t.Fatalf("models = %v, want claude: [sonnet opus]", got.Models)
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

	models := map[string][]string{"claude": {"m"}}
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
	models := map[string][]string{"claude": {"a", "b"}}
	if err := Project(s, contracts.Event{Payload: contracts.AutoApprovedModelsSet{Models: models}}); err != nil {
		t.Fatal(err)
	}
	if len(s.AutoApprovedModels["claude"]) != 2 || s.AutoApprovedModels["claude"][0] != "a" {
		t.Fatalf("models = %v, want claude: [a b]", s.AutoApprovedModels)
	}
	// The projector copies the lists, so mutating the event payload later does
	// not change the projected state.
	models["claude"][0] = "changed"
	if s.AutoApprovedModels["claude"][0] != "a" {
		t.Fatalf("projected lists alias the event: %v", s.AutoApprovedModels)
	}

	if err := Project(s, contracts.Event{Payload: contracts.AutoApprovedModelsSet{}}); err != nil {
		t.Fatal(err)
	}
	if len(s.AutoApprovedModels) != 0 {
		t.Fatalf("clearing should leave no models: %v", s.AutoApprovedModels)
	}
}
