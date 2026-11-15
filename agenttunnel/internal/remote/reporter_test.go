package remote

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// Reporter mode sends one status report when connectivity is established:
// enabled, the master's report rides the same ordered worker as every other
// outbound update and reaches the adapter as a marked message.
func TestBridgeReporterSendsStatusReportOnConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	adapter := &fakeAdapter{sentCh: make(chan string, 8)}
	b, err := New(Options{
		StateDir: t.TempDir(), Adapter: adapter,
		Reporter:        func(context.Context) string { return "AgentTunnel status\n\nAll quiet." },
		ReporterEnabled: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)
	b.Start(ctx)

	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}

	got := await(t, adapter.sentCh, "the reporter status report")
	if !strings.HasPrefix(got, outboundMarker+" AgentTunnel status") {
		t.Fatalf("report = %q, want the marker then the status report", got)
	}

	// Reconnecting after a disconnect reports again: connectivity was
	// re-established.
	if err := b.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if err := b.Connect(ctx); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	second := await(t, adapter.sentCh, "the reporter report after reconnecting")
	if !strings.Contains(second, "All quiet.") {
		t.Fatalf("second report = %q", second)
	}
}

// Reporter mode does no work at all while disabled: the report closure never
// runs and nothing is enqueued for the adapter.
func TestBridgeReporterSkipsWorkWhenDisabled(t *testing.T) {
	ctx := context.Background()
	adapter := &fakeAdapter{sentCh: make(chan string, 8)}
	var ran atomic.Bool
	b, err := New(Options{
		StateDir: t.TempDir(), Adapter: adapter,
		Reporter: func(context.Context) string {
			ran.Store(true)
			return "should never be built"
		},
		ReporterEnabled: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)

	if err := b.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := b.Status().State; got != string(StateConnected) {
		t.Fatalf("state = %q, want connected", got)
	}
	// maybeReport ran synchronously inside Connect, so by the time it returns
	// the closure either ran or it never will.
	if ran.Load() {
		t.Fatal("the report closure ran while reporter mode was disabled")
	}
	if n := len(b.out); n != 0 {
		t.Fatalf("out queue holds %d messages, want none", n)
	}
	select {
	case text := <-adapter.sentCh:
		t.Fatalf("disabled reporter sent %q", text)
	default:
	}
}

// An empty report is dropped rather than sending a blank message.
func TestBridgeReporterDropsEmptyReport(t *testing.T) {
	adapter := &fakeAdapter{sentCh: make(chan string, 8)}
	b, err := New(Options{
		StateDir: t.TempDir(), Adapter: adapter,
		Reporter:        func(context.Context) string { return "   " },
		ReporterEnabled: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}
	t.Cleanup(b.Close)

	if err := b.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	// The decision happens synchronously in Connect, before any worker could
	// pick the message up, so the empty queue proves the drop.
	if n := len(b.out); n != 0 {
		t.Fatalf("out queue holds %d messages, want the empty report dropped", n)
	}
	select {
	case text := <-adapter.sentCh:
		t.Fatalf("empty report still sent %q", text)
	default:
	}
}
