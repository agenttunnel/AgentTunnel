package daemon

// The real mobile client (mobile/src/lib/relay.ts — pure TS over the global
// WebSocket, no React Native imports) driven against a real atrelay binary and
// a daemon with a temp StateDir. Until now the mobile app only ever ran
// against the fake socket in mobile/scripts/smoke.ts; this proves the actual
// exchangePairCode and RpcClient wire behavior end to end, through bun running
// mobile/scripts/relay-live.ts. The test skips when bun is absent so CI
// without it stays green.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mobileScriptEnv is a child environment without this session's agent
// variables, so nothing spawned here can mistake itself for an agent.
func mobileScriptEnv(extra ...string) []string {
	out := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "AT_AGENT_ID=") || strings.HasPrefix(kv, "AT_THREAD_ID=") ||
			strings.HasPrefix(kv, "AT_DAEMON_URL=") || strings.HasPrefix(kv, "AT_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}

// TestMobileClientInteropThroughRealAtrelay runs bun with the committed
// relay-live.ts script: the desktop mints a code, the real mobile client pairs,
// attaches, creates and lists a thread, runs a turn (reply only at turn end,
// no-timeout path) with a steer in between, asserts thread.event frames land,
// and is then rejected when the desktop unpairs it — with no retry loop.
func TestMobileClientInteropThroughRealAtrelay(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skipf("bun is not installed, mobile interop needs it: %v", err)
	}

	base, _ := startAtrelayProcess(t)
	d := newTestDaemon(t)
	desk := dialAt(t, wsBase(t, d), "secret")

	call(t, desk, "s1", "relay.enable", map[string]any{"url": base})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Relay.WaitConnected(ctx); err != nil {
		t.Fatalf("daemon never connected to the relay: %v", err)
	}

	var offer struct {
		Code string `json:"code"`
	}
	json.Unmarshal(call(t, desk, "p1", "relay.pair.start", nil), &offer)
	if offer.Code == "" {
		t.Fatal("pairing start returned no code")
	}

	mobileDir, err := filepath.Abs(filepath.Join("..", "..", "mobile"))
	if err != nil {
		t.Fatalf("resolve mobile dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mobileDir, "scripts", "relay-live.ts")); err != nil {
		t.Fatalf("scripts/relay-live.ts: %v", err)
	}

	cmd := exec.Command(bun, "run", "scripts/relay-live.ts")
	cmd.Dir = mobileDir
	cmd.Env = mobileScriptEnv(
		"AT_RELAY_URL="+base,
		"AT_PAIR_CODE="+offer.Code,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("script stdout: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("script stderr: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bun: %v", err)
	}
	done := make(chan error, 1)
	errLines := make(chan string, 16)
	go func() {
		done <- cmd.Wait()
	}()
	go func() {
		defer close(errLines)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			// Logged immediately: bun's own errors are the first place to
			// look when the script misbehaves.
			t.Logf("script stderr: %s", sc.Text())
			errLines <- sc.Text()
		}
	}()
	// The body consumes the exit error at the end, and cleanup waits for it
	// too; once-ify the receive so a second wait never blocks.
	waitExit := sync.OnceValue(func() error { return <-done })
	t.Cleanup(func() {
		// Kill only the PID captured at spawn; never by pattern.
		_ = cmd.Process.Kill()
		_ = waitExit()
	})

	lines := make(chan string, 32)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	next := func(want string) string {
		t.Helper()
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("script output ended before %q", want)
			}
			return line
		case <-time.After(60 * time.Second):
			t.Fatalf("script stalled before %q", want)
		}
		return ""
	}

	// The real mobile client pairs with the code minted over /ws.
	if line := next("paired"); line != "paired" {
		t.Fatalf("script: %q", line)
	}

	// The desktop sees the phone among its devices, with the name it sent.
	var devices struct {
		Devices []struct {
			ClientID string `json:"clientId"`
			Name     string `json:"name"`
		} `json:"devices"`
	}
	json.Unmarshal(call(t, desk, "d1", "relay.devices.list", nil), &devices)
	if len(devices.Devices) != 1 || devices.Devices[0].Name != "QA bun client" || devices.Devices[0].ClientID == "" {
		t.Fatalf("desktop should list the phone the script paired, got %+v", devices.Devices)
	}

	for _, want := range []string{"open", "listed", "steered", "turn-done"} {
		if line := next(want); line != want {
			t.Fatalf("script at %s: %q", want, line)
		}
	}
	ev := next("events N")
	if !strings.HasPrefix(ev, "events ") || ev == "events 0" {
		t.Fatalf("script should have received event frames, got %q", ev)
	}
	if line := next("READY-FOR-UNPAIR"); line != "READY-FOR-UNPAIR" {
		t.Fatalf("script: %q", line)
	}

	// The desktop unpairs the phone while it is attached and idle.
	json.Unmarshal(call(t, desk, "u1", "relay.unpair", map[string]any{"clientId": devices.Devices[0].ClientID}), &devices)
	if len(devices.Devices) != 0 {
		t.Fatalf("unpair should empty the device list, got %+v", devices.Devices)
	}

	for _, want := range []string{"REVOKED-UNPAIRED", "NO-RETRY-LOOP", "INTEROP-OK"} {
		if line := next(want); line != want {
			t.Fatalf("script at %s: %q", want, line)
		}
	}
	if err := waitExit(); err != nil {
		for l := range errLines {
			t.Logf("script stderr: %s", l)
		}
		t.Fatalf("bun script failed: %v", err)
	}
}
