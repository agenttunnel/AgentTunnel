package transport

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAppInfoRPCReportsVersion(t *testing.T) {
	disp := NewDispatcher(DispatcherDeps{Version: "1.2.3"})

	res, err := disp.Handle(context.Background(), Request{ID: "1", Method: "app.info"})
	if err != nil {
		t.Fatalf("app.info: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got.Version != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", got.Version)
	}
}

func TestAppInfoRPCFallsBackToDev(t *testing.T) {
	disp := NewDispatcher(DispatcherDeps{})

	res, err := disp.Handle(context.Background(), Request{ID: "1", Method: "app.info"})
	if err != nil {
		t.Fatalf("app.info: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got.Version != "dev" {
		t.Fatalf("version = %q, want dev", got.Version)
	}
}
