package transport

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// stubLimits reports one available harness and one unavailable harness.
type stubLimits struct{}

func (stubLimits) Harness() contracts.Harness { return contracts.HarnessClaude }

func (stubLimits) Fetch(context.Context) ([]contracts.LimitWindow, string) {
	return []contracts.LimitWindow{{ID: "five_hour", Label: "5-hour", UsedPercent: 37}}, ""
}

type missingLimits struct{}

func (missingLimits) Harness() contracts.Harness { return contracts.HarnessCursor }

func (missingLimits) Fetch(context.Context) ([]contracts.LimitWindow, string) {
	return nil, "not signed in to Cursor"
}

func TestUsageLimitsRPCReportsEveryHarness(t *testing.T) {
	disp := NewDispatcher(DispatcherDeps{Limits: providers.NewLimitsProvider(stubLimits{}, missingLimits{})})

	res, err := disp.Handle(context.Background(), Request{ID: "1", Method: "usage.limits", Params: json.RawMessage(`{"refresh":true}`)})
	if err != nil {
		t.Fatalf("usage.limits: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Harness   string `json:"harness"`
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
		Windows   []struct {
			ID          string  `json:"id"`
			Label       string  `json:"label"`
			UsedPercent float64 `json:"usedPercent"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d harnesses, want 2: %s", len(got), raw)
	}
	if got[0].Harness != "claude" || !got[0].Available || len(got[0].Windows) != 1 || got[0].Windows[0].UsedPercent != 37 {
		t.Fatalf("claude entry = %+v", got[0])
	}
	if got[1].Harness != "cursor" || got[1].Available || got[1].Reason == "" || got[1].Windows != nil {
		t.Fatalf("unavailable entry should still reply successfully, got %+v", got[1])
	}
}

func TestUsageLimitsRPCWithoutProviderReturnsEmptyList(t *testing.T) {
	disp := NewDispatcher(DispatcherDeps{})
	res, err := disp.Handle(context.Background(), Request{ID: "1", Method: "usage.limits"})
	if err != nil {
		t.Fatalf("usage.limits: %v", err)
	}
	if list, ok := res.([]contracts.HarnessLimits); !ok || len(list) != 0 {
		t.Fatalf("res = %#v, want an empty list", res)
	}
}
