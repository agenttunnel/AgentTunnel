package daemon

// The real atrelay BINARY as a separate process: relay_e2e_test.go runs
// pkg/relay in-process, so this file proves the shipped cmd/atrelay artifact
// itself — its flags, its "listening on" log line, a file-backed state path —
// carries pair -> attach -> steer inside a turn -> thread.event frames ->
// reconnect resuming events -> unpair closing the socket, against a daemon
// with a temp StateDir. The binary is built to a temp dir, and only the PID
// captured at spawn is ever killed. Waits are on typed frames and replies,
// never sleeps.
//
// It shares pairPhone/attachPhone/relayDial/writeRelay/relayFrameOfType with
// relay_e2e_test.go; those helpers are transport-agnostic.

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

	relaywire "github.com/agenttunnel/agenttunnel-go/pkg/relay"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

var (
	atrelayBinOnce sync.Once
	atrelayBinPath string
	atrelayBinErr  error
)

// atrelayBinary compiles cmd/atrelay once for the package's tests. The binary
// is the artifact a user runs, not a test double.
func atrelayBinary(t *testing.T) string {
	t.Helper()
	atrelayBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "qa-atrelay-bin-")
		if err != nil {
			atrelayBinErr = err
			return
		}
		out := filepath.Join(dir, "atrelay")
		cmd := exec.Command("go", "build", "-o", out, "github.com/agenttunnel/agenttunnel-go/cmd/atrelay")
		if data, err := cmd.CombinedOutput(); err != nil {
			atrelayBinErr = err
			t.Logf("atrelay build output:\n%s", data)
			return
		}
		atrelayBinPath = out
	})
	if atrelayBinErr != nil {
		t.Fatalf("build the atrelay binary: %v", atrelayBinErr)
	}
	return atrelayBinPath
}

// startAtrelayProcess runs the actual atrelay binary on 127.0.0.1:0 with a
// temp state file and returns its ws base URL, parsed from the
// "atrelay listening on" line it logs. Cleanup kills only the captured PID.
func startAtrelayProcess(t *testing.T) (base, statePath string) {
	t.Helper()
	bin := atrelayBinary(t)
	statePath = filepath.Join(t.TempDir(), "relay-state.json")
	cmd := exec.Command(bin, "-listen", "127.0.0.1:0", "-state", statePath)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("atrelay stderr: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start atrelay: %v", err)
	}
	done := make(chan error, 1)
	dead := make(chan struct{})
	go func() {
		done <- cmd.Wait()
		close(dead)
	}()
	// The listening loop consumes an early death, the body the final exit, and
	// cleanup waits too; once-ify the receive so a second wait never blocks.
	waitExit := sync.OnceValue(func() error { return <-done })
	t.Cleanup(func() {
		// Kill only the PID captured at spawn; never by pattern.
		_ = cmd.Process.Kill()
		_ = waitExit()
	})

	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			t.Logf("atrelay: %s", sc.Text())
			lines <- sc.Text()
		}
	}()

	const marker = "atrelay listening on "
	deadline := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("atrelay exited before listening: %v", <-done)
			}
			// The line is log-formatted: "2006/01/02 15:04:05 atrelay listening on …".
			if i := strings.Index(line, marker); i >= 0 {
				return "ws://" + strings.TrimSpace(line[i+len(marker):]), statePath
			}
		case <-dead:
			t.Fatalf("atrelay process died: %v", waitExit())
		case <-deadline:
			t.Fatal("atrelay never logged its listen address")
		}
	}
}

// TestRealAtrelayBinaryPairsSteersUnpairs runs the whole relay flow against the
// atrelay binary as a separate process: wrong code rejected, code single-use,
// steer inside a running turn, reconnect resuming thread.event, unpair closing
// the socket, and no message body in the relay's persisted state.
func TestRealAtrelayBinaryPairsSteersUnpairs(t *testing.T) {
	base, statePath := startAtrelayProcess(t)
	d := newTestDaemon(t)
	desk := dialAt(t, wsBase(t, d), "secret")

	// Off by default, and enabling opens no listener on the daemon.
	var status struct {
		Enabled   bool `json:"enabled"`
		Connected bool `json:"connected"`
	}
	json.Unmarshal(call(t, desk, "s0", "relay.status", nil), &status)
	if status.Enabled || status.Connected {
		t.Fatalf("relay must start off and disconnected, got %+v", status)
	}

	call(t, desk, "s1", "relay.enable", map[string]any{"url": base})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Relay.WaitConnected(ctx); err != nil {
		t.Fatalf("daemon never connected to the atrelay binary: %v", err)
	}

	// A wrong code is refused by the real relay.
	var offer struct {
		Code string `json:"code"`
	}
	json.Unmarshal(call(t, desk, "p1", "relay.pair.start", nil), &offer)
	if offer.Code == "" {
		t.Fatal("pairing start returned no code")
	}
	wrong := relayDial(t, base)
	writeRelay(t, wrong, relaywire.Pair{T: "pair", Code: "ZZZZZ-ZZZZZ", Name: "attacker"})
	if ack := relayFrameOfType(t, wrong, "ack"); ack["error"] != relaywire.ErrBadCode {
		t.Fatalf("a wrong code must fail as bad_code, got %v", ack)
	}

	token, clientID := pairPhone(t, base, offer.Code)

	// Single use: replaying the same code fails.
	again := relayDial(t, base)
	writeRelay(t, again, relaywire.Pair{T: "pair", Code: offer.Code, Name: "replay"})
	if ack := relayFrameOfType(t, again, "ack"); ack["error"] != relaywire.ErrBadCode {
		t.Fatalf("a used code must fail as bad_code, got %v", ack)
	}

	// Attach and drive the daemon through the real relay.
	phone := attachPhone(t, base, token)
	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	const threadTitle = "binary relay thread"
	json.Unmarshal(call(t, phone, "1", "thread.create", map[string]any{"title": threadTitle}), &created)
	call(t, phone, "2", "thread.subscribe", map[string]any{"threadId": created.ThreadID})

	var threads []struct {
		ID contracts.ThreadID `json:"id"`
	}
	json.Unmarshal(call(t, phone, "3", "thread.list", nil), &threads)
	if !containsThread(threads, created.ThreadID) {
		t.Fatalf("phone should list its thread through the binary relay, got %+v", threads)
	}

	// One turn, steered in the middle while it runs.
	const turnOne = "turn one over the real binary"
	startTurn(t, phone, "4", string(created.ThreadID), turnOne)
	const steerText = "steer it mid-turn"
	call(t, phone, "5", "thread.steer", map[string]any{"threadId": created.ThreadID, "text": steerText})
	waitForEvent(t, phone, contracts.EvtAgentOutput)
	waitForEvent(t, phone, contracts.EvtTaskDispatched)
	waitForReply(t, phone, "4")

	// The paired client drops and comes back: thread.event frames resume on
	// a turn started after the reconnect.
	phone.CloseNow()
	phone2 := attachPhone(t, base, token)
	call(t, phone2, "6", "thread.subscribe", map[string]any{"threadId": created.ThreadID})
	json.Unmarshal(call(t, phone2, "7", "thread.list", nil), &threads)
	if !containsThread(threads, created.ThreadID) {
		t.Fatalf("thread should survive the reconnect, got %+v", threads)
	}
	const turnTwo = "turn two after reconnect"
	startTurn(t, phone2, "8", string(created.ThreadID), turnTwo)
	waitForEvent(t, phone2, contracts.EvtMessageSent)
	waitForEvent(t, phone2, contracts.EvtAgentOutput)
	waitForReply(t, phone2, "8")

	// Unpair from the desktop: the phone's socket closes with bye revoked.
	var devices struct {
		Devices []struct {
			ClientID string `json:"clientId"`
		} `json:"devices"`
	}
	json.Unmarshal(call(t, desk, "u1", "relay.unpair", map[string]any{"clientId": clientID}), &devices)
	if len(devices.Devices) != 0 {
		t.Fatalf("unpair should empty the device list, got %+v", devices.Devices)
	}
	bye := relayFrameOfType(t, phone2, "bye")
	if bye["reason"] != "revoked" {
		t.Fatalf("phone should be told it was revoked, got %v", bye)
	}
	if _, _, err := phone2.Read(context.Background()); err == nil {
		t.Fatal("the revoked phone socket must close")
	}

	// The relay persists no message bodies: after two turns, a steer, and
	// event routing, its state file holds neither prompts nor thread titles.
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read relay state: %v", err)
	}
	for _, marker := range []string{turnOne, turnTwo, steerText, threadTitle} {
		if strings.Contains(string(data), marker) {
			t.Fatalf("relay state leaked the message body %q", marker)
		}
	}
}
