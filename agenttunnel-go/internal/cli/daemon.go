package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/daemonlock"
	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

// atdEnvVars are the variables a spawned daemon must never inherit: agent
// session markers (they would make it believe it runs inside an agent, or point
// it at another daemon's socket and token) and a stale AT_AUTOSTART marker,
// which this process sets explicitly for the daemon it starts.
var atdEnvVars = []string{"AT_AGENT_ID", "AT_THREAD_ID", "AT_DAEMON_URL", "AT_TOKEN", "AT_TOOL", "AT_AUTOSTART"}

// scrubEnv removes the agent-session variables from an environment block.
func scrubEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		drop := false
		for _, key := range atdEnvVars {
			if strings.HasPrefix(entry, key+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return out
}

// atdBinary finds the daemon to autostart: beside this executable first (an
// installed release ships them together), then on PATH.
func atdBinary() (string, error) {
	if p := os.Getenv("AT_ATD"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, name := range []string{"atd", "atd.exe"} {
			cand := filepath.Join(dir, name)
			if info, err := os.Stat(cand); err == nil && !info.IsDir() {
				return cand, nil
			}
		}
	}
	return exec.LookPath("atd")
}

// spawnDaemon starts `atd --port 0 --home-dir <state>` detached from this
// process: it keeps running after the CLI exits and has no inherited agent
// environment.
func spawnDaemon(stateDir string) error {
	bin, err := atdBinary()
	if err != nil {
		return fmt.Errorf("atd was not found beside %s or on PATH: %w", os.Args[0], err)
	}
	cmd := exec.Command(bin, "--port", "0", "--home-dir", stateDir)
	// AT_AUTOSTART tells atd that this process already holds daemon.lock, so it
	// must not wait on that lock before binding.
	cmd.Env = append(scrubEnv(os.Environ()), "AT_AUTOSTART=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	// The daemon is not our child to wait on; releasing keeps a CLI exit from
	// leaving it as a zombie.
	_ = cmd.Process.Release()
	return nil
}

// connectExisting dials the daemon in <state>/daemon.json and pings it. A
// missing, unreadable, or unresponsive file reports ErrNoDaemon.
func (e *Env) connectExisting(ctx context.Context) (*Client, discovery.Info, error) {
	info, err := discovery.Read(e.StateDir())
	if err != nil {
		return nil, discovery.Info{}, ErrNoDaemon
	}
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := Dial(dctx, info.WSURL, info.Token)
	if err != nil {
		return nil, info, ErrNoDaemon
	}
	if err := c.Ping(dctx); err != nil {
		c.Close()
		return nil, info, ErrNoDaemon
	}
	return c, info, nil
}

// Connect returns a live client, starting a detached daemon when none answers.
// An explicit --daemon never autostarts: a URL the user gave is the only place
// to talk to.
func (e *Env) Connect(ctx context.Context) (*Client, discovery.Info, error) {
	if e.DaemonURL != "" {
		token := os.Getenv("AT_TOKEN")
		if token == "" {
			if info, err := discovery.Read(e.StateDir()); err == nil && info.WSURL == e.DaemonURL {
				token = info.Token
			}
		}
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		c, err := Dial(dctx, e.DaemonURL, token)
		if err != nil {
			return nil, discovery.Info{WSURL: e.DaemonURL, Token: token}, fmt.Errorf("connect to %s: %w", e.DaemonURL, err)
		}
		if err := c.Ping(dctx); err != nil {
			c.Close()
			return nil, discovery.Info{WSURL: e.DaemonURL, Token: token}, fmt.Errorf("%s did not answer: %w", e.DaemonURL, err)
		}
		return c, discovery.Info{WSURL: e.DaemonURL, Token: token}, nil
	}

	// One process at a time decides whether a daemon must start. The winner
	// starts it and publishes discovery before releasing, so a competing `agenttunnel`
	// that was waiting attaches to that daemon instead of starting a second one
	// on the same state dir.
	lock, err := daemonlock.Acquire(ctx, e.StateDir(), 50*time.Millisecond)
	if err != nil {
		return nil, discovery.Info{}, err
	}
	defer lock.Release()

	// Re-check under the lock: the winner may have brought a daemon up while
	// this process waited for the lock.
	if c, info, err := e.connectExisting(ctx); err == nil {
		return c, info, nil
	}

	// Autostart from inside an agent session would nest daemons and share the
	// environment they rely on; refuse loudly instead.
	if os.Getenv("AT_AGENT_ID") != "" {
		return nil, discovery.Info{}, errors.New(
			"no AgentTunnel daemon is running, and this shell belongs to an agent session (AT_AGENT_ID is set). " +
				"Refusing to start a nested daemon; start one with `atd` or pass --daemon <ws-url>")
	}
	if err := spawnDaemon(e.StateDir()); err != nil {
		return nil, discovery.Info{}, err
	}
	c, info, err := e.waitForDaemon(ctx, 10*time.Second)
	if err != nil {
		return nil, info, fmt.Errorf("started atd but it did not answer within 10s: %w", err)
	}
	return c, info, nil
}

// waitForDaemon polls the discovery file and pings the daemon until it is up or
// the deadline passes.
func (e *Env) waitForDaemon(ctx context.Context, timeout time.Duration) (*Client, discovery.Info, error) {
	deadline := time.Now().Add(timeout)
	for {
		info, err := discovery.Read(e.StateDir())
		if err == nil {
			dctx, cancel := context.WithTimeout(ctx, 1*time.Second)
			c, derr := Dial(dctx, info.WSURL, info.Token)
			if derr == nil {
				perr := c.Ping(dctx)
				cancel()
				if perr == nil {
					return c, info, nil
				}
				c.Close()
			} else {
				cancel()
			}
		}
		if time.Now().After(deadline) {
			return nil, discovery.Info{}, ErrNoDaemon
		}
		select {
		case <-ctx.Done():
			return nil, discovery.Info{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
