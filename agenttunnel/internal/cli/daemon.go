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
	"github.com/agenttunnel/agenttunnel-go/internal/logrotate"
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

// atdLogPath is where an autostarted daemon writes its output. The daemon is
// detached, so a boot failure has nowhere else to say why.
func atdLogPath(stateDir string) string { return filepath.Join(stateDir, "atd.log") }

// openAtdLog returns the file the spawned daemon's stdio goes to, after writing
// a start marker through the same rotating writer the daemon uses (--log-file).
// It returns nil when the log cannot be opened: a daemon that can boot without
// a log file must still boot, so spawnDaemon then omits --log-file.
func openAtdLog(stateDir string) *os.File {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil
	}
	path := atdLogPath(stateDir)
	w, err := logrotate.New(path, logrotate.DefaultMaxBytes)
	if err != nil {
		return nil
	}
	fmt.Fprintf(w, "--- %s agenttunnel started atd\n", time.Now().UTC().Format(time.RFC3339))
	_ = w.Close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// spawnDaemon starts `atd --port 0 --home-dir <state>` detached from this
// process: it keeps running after the CLI exits and has no inherited agent
// environment. Its log output goes to atd.log in the state dir through the
// daemon's own rotating writer (--log-file), so autostart output stays bounded.
// Stdio also points at atd.log to catch anything written before logging is set
// up; that handle is not rotated, so such stray output is only the boot window.
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
	// A nil io.Writer (not a nil *os.File) discards output when no log opens.
	if logFile := openAtdLog(stateDir); logFile != nil {
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.Args = append(cmd.Args, "--log-file", atdLogPath(stateDir))
	}
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	// The daemon is not our child to wait on; releasing keeps a CLI exit from
	// leaving it as a zombie.
	_ = cmd.Process.Release()
	return nil
}

// Causes a read-only command can report when the daemon in daemon.json is not
// usable. errNotRunning is ErrNoDaemon, so callers that only ask "is there a
// daemon" keep working.
var (
	errRejected    = errors.New("the daemon rejected the token in daemon.json")
	errUnreachable = errors.New("the daemon in daemon.json is not answering")
)

// errNotRunning reports that no daemon.json exists.
var errNotRunning = ErrNoDaemon

// unauthorizedDial reports whether a dial failed on the HTTP handshake with 401.
// coder/websocket returns no typed error for a non-101 handshake, so the status
// is matched on its message. daemon_test pins this against a real server.
func unauthorizedDial(err error) bool {
	return err != nil && strings.Contains(err.Error(), "expected handshake response status code 101 but got 401")
}

// dialExisting reads <state>/daemon.json, dials the daemon it names, and pings
// it. The error names the cause: no file, a rejected token, or a file whose
// daemon does not answer (stale, exited, or hung).
func (e *Env) dialExisting(ctx context.Context) (*Client, discovery.Info, error) {
	info, err := discovery.Read(e.StateDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, discovery.Info{}, errNotRunning
	}
	if err != nil {
		return nil, discovery.Info{}, errUnreachable
	}
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := Dial(dctx, info.WSURL, info.Token)
	if err != nil {
		if unauthorizedDial(err) {
			return nil, info, errRejected
		}
		return nil, info, errUnreachable
	}
	if err := c.Ping(dctx); err != nil {
		c.Close()
		return nil, info, errUnreachable
	}
	return c, info, nil
}

// connectExisting dials the daemon in <state>/daemon.json and pings it. Every
// failure reports ErrNoDaemon; connectReadOnly keeps the cause.
func (e *Env) connectExisting(ctx context.Context) (*Client, discovery.Info, error) {
	c, info, err := e.dialExisting(ctx)
	if err != nil {
		return nil, discovery.Info{}, ErrNoDaemon
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
		return nil, info, fmt.Errorf("started atd but it did not answer within 10s (its output is in %s): %w", atdLogPath(e.StateDir()), err)
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
