package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

// answeringDaemon accepts one WebSocket and answers each RPC with an empty
// result, which is all daemonlock.Live needs to see a live daemon.
func answeringDaemon(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &req)
		resp, _ := json.Marshal(map[string]any{"id": req.ID, "result": []any{}})
		_ = conn.Write(ctx, websocket.MessageText, resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// liveDaemonState returns a state dir whose discovery file points at a daemon
// that answers.
func liveDaemonState(t *testing.T) string {
	t.Helper()
	srv := answeringDaemon(t)
	state := t.TempDir()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if err := discovery.Write(state, discovery.Info{WSURL: wsURL, Token: "tok"}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	return state
}

func TestYieldToLiveDaemonDetectsAnsweringDaemon(t *testing.T) {
	if !yieldToLiveDaemon(context.Background(), liveDaemonState(t)) {
		t.Fatal("a live daemon on the state dir must yield")
	}
}

// With no daemon answering, the start proceeds instead of yielding.
func TestYieldToLiveDaemonProceedsWithoutLiveDaemon(t *testing.T) {
	state := t.TempDir()
	if yieldToLiveDaemon(context.Background(), state) {
		t.Fatal("no discovery file: start must proceed")
	}
	// A recorded but unreachable endpoint is not live either.
	if err := discovery.Write(state, discovery.Info{WSURL: "ws://127.0.0.1:1/ws", Token: "tok"}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	if yieldToLiveDaemon(context.Background(), state) {
		t.Fatal("unreachable discovery endpoint: start must proceed")
	}
}

// yieldHelperEnv names the state dir for the child process spawned by
// TestLiveDaemonYieldsWithExitZeroAndNoOutput. The child runs the real main().
const yieldHelperEnv = "ATD_YIELD_HELPER_STATE"

// TestAtdHelperProcess is not a test on its own: the parent test re-executes
// this binary with yieldHelperEnv set, and this runs the real atd main() with
// the flags a service manager would pass.
func TestAtdHelperProcess(t *testing.T) {
	state := os.Getenv(yieldHelperEnv)
	if state == "" {
		t.Skip("helper process only")
	}
	os.Args = []string{"atd", "--home-dir", state, "--port", "0"}
	main()
}

// A start that finds a live daemon on its state dir exits 0 with no output, so
// a restart-on-failure service manager does not respawn it while a desktop
// daemon owns the state dir.
func TestLiveDaemonYieldsWithExitZeroAndNoOutput(t *testing.T) {
	state := liveDaemonState(t)

	// Safety bound only: if the yield regresses, main would serve forever and
	// the parent must not hang. A correct yield exits well before this.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAtdHelperProcess$")
	cmd.Env = append(scrubbedAgentEnv(), yieldHelperEnv+"="+state)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("child did not exit: yield regressed to serving (stderr: %q)", stderr.String())
	}
	if err != nil {
		t.Fatalf("yield exit status: %v (stderr: %q)", err, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("yield must be silent, got stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

// scrubbedAgentEnv is the parent environment without AT_* entries, so an agent
// session running these tests cannot trip the AT_AGENT_ID refusal or autostart
// handoff inside the child.
func scrubbedAgentEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AT_") {
			env = append(env, kv)
		}
	}
	return env
}
