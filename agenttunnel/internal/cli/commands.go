package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

func init() {
	Register(Command{
		Name:    "daemon",
		Summary: "Manage the background AgentTunnel daemon",
		Subcommands: []Command{
			{Name: "start", Summary: "Start a detached daemon if one is not running", Usage: "agenttunnel daemon start", Run: runDaemonStart},
			{Name: "status", Summary: "Show whether a daemon is running", Usage: "agenttunnel daemon status", Run: runDaemonStatus},
			{Name: "stop", Summary: "Stop the running daemon and wait for it to exit", Usage: "agenttunnel daemon stop", Run: runDaemonStop},
		},
	})
	Register(Command{Name: "version", Summary: "Print the agenttunnel version", Usage: "agenttunnel version", Run: runVersion})
}

// daemonView is the machine-readable daemon status.
type daemonView struct {
	Running   bool       `json:"running"`
	WSURL     string     `json:"wsUrl,omitempty"`
	HTTPURL   string     `json:"httpUrl,omitempty"`
	PID       int        `json:"pid,omitempty"`
	Version   string     `json:"version,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

func runVersion(_ context.Context, env *Env, _ []string) error {
	if env.JSON {
		return env.PrintJSON(map[string]string{"version": env.Version})
	}
	fmt.Fprintln(env.Out, "agenttunnel "+env.Version)
	return nil
}

func runDaemonStart(ctx context.Context, env *Env, _ []string) error {
	c, info, err := env.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	return printRunning(env, enrichInfo(env, info))
}

func runDaemonStatus(ctx context.Context, env *Env, _ []string) error {
	if env.DaemonURL != "" {
		c, info, err := env.Connect(ctx)
		if err != nil {
			return err
		}
		defer c.Close()
		return printRunning(env, info)
	}
	info, err := discovery.Read(env.StateDir())
	if err != nil {
		return printNotRunning(env)
	}
	c, _, err := env.connectExisting(ctx)
	if err != nil {
		return printNotRunning(env)
	}
	c.Close()
	return printRunning(env, info)
}

func runDaemonStop(ctx context.Context, env *Env, _ []string) error {
	// An explicit URL may be this machine's own daemon, in which case its
	// discovery record supplies the pid. Read it before shutdown removes it.
	if env.DaemonURL != "" {
		c, got, err := env.Connect(ctx)
		if err != nil {
			return err
		}
		pid := enrichInfo(env, discovery.Info{WSURL: env.DaemonURL}).PID
		err = shutdownClient(ctx, c)
		c.Close()
		if err != nil {
			return err
		}
		// Wait as the discovery path does: a script's next command must see the
		// daemon gone. A remote URL has no file, so probe until it stops answering.
		if err := waitFor(ctx, 10*time.Second, func() bool {
			return !answersAt(ctx, env.DaemonURL, got.Token)
		}); err != nil {
			return stopTimeoutErr(ctx, err)
		}
		if env.JSON {
			// wsUrl is the legacy key scripts read; it stays beside pid.
			return env.PrintJSON(map[string]any{"ok": true, "stopped": true, "pid": pid, "wsUrl": env.DaemonURL})
		}
		fmt.Fprintln(env.Out, "daemon stopped")
		return nil
	}

	if _, err := discovery.Read(env.StateDir()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return printNotRunning(env)
		}
		return err
	}
	c, info, err := env.connectExisting(ctx)
	if err != nil {
		fmt.Fprintln(env.Err, "agenttunnel: daemon.json is stale (the daemon is not answering)")
		return failure()
	}
	if err := shutdownClient(ctx, c); err != nil {
		c.Close()
		return err
	}
	c.Close()

	// The reply is written before the daemon tears down; the file is removed on
	// the way out. Wait for it so a script's next command sees a clean state.
	if err := waitFor(ctx, 10*time.Second, func() bool {
		_, err := os.Stat(discovery.Path(env.StateDir()))
		return errors.Is(err, os.ErrNotExist)
	}); err != nil {
		return stopTimeoutErr(ctx, err)
	}
	if env.JSON {
		return env.PrintJSON(map[string]any{"ok": true, "stopped": true, "pid": info.PID})
	}
	fmt.Fprintln(env.Out, "daemon stopped")
	return nil
}

// errWaitTimeout reports that waitFor's condition did not hold before its deadline.
var errWaitTimeout = errors.New("timed out")

// waitFor polls done every 50ms. It returns nil once done reports true,
// errWaitTimeout at the deadline, or the context's error if it is cancelled.
func waitFor(ctx context.Context, timeout time.Duration, done func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if done() {
			return nil
		}
		if time.Now().After(deadline) {
			return errWaitTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// stopTimeoutErr turns a waitFor failure into the message both stop paths use.
func stopTimeoutErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, errWaitTimeout) {
		return errors.New("daemon acknowledged shutdown but did not exit within 10s")
	}
	return err
}

// answersAt reports whether a daemon at wsURL still answers RPCs.
func answersAt(ctx context.Context, wsURL, token string) bool {
	dctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := Dial(dctx, wsURL, token)
	if err != nil {
		return false
	}
	defer c.Close()
	return c.Ping(dctx) == nil
}

// shutdownClient sends daemon.shutdown and verifies the acknowledgement.
func shutdownClient(ctx context.Context, c *Client) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var res struct {
		OK bool `json:"ok"`
	}
	if err := c.Call(cctx, "daemon.shutdown", nil, &res); err != nil {
		return fmt.Errorf("daemon.shutdown: %w", err)
	}
	if !res.OK {
		return errors.New("daemon did not acknowledge shutdown")
	}
	return nil
}

// enrichInfo prefers the full discovery record when it describes the daemon we
// are talking to, so pid/version/startedAt are filled in for --daemon too.
func enrichInfo(env *Env, got discovery.Info) discovery.Info {
	info, err := discovery.Read(env.StateDir())
	if err == nil && info.WSURL == got.WSURL {
		return info
	}
	return got
}

func printRunning(env *Env, info discovery.Info) error {
	if env.JSON {
		view := daemonView{
			Running: true, WSURL: info.WSURL, HTTPURL: info.HTTPURL,
			PID: info.PID, Version: info.Version,
		}
		if !info.StartedAt.IsZero() {
			t := info.StartedAt
			view.StartedAt = &t
		}
		return env.PrintJSON(view)
	}
	line := "daemon running at " + info.WSURL
	if info.PID > 0 {
		line += fmt.Sprintf(" (pid %d", info.PID)
		if info.Version != "" {
			line += ", version " + info.Version
		}
		line += ")"
	} else if info.Version != "" {
		line += " (version " + info.Version + ")"
	}
	fmt.Fprintln(env.Out, line)
	return nil
}

func printNotRunning(env *Env) error {
	if env.JSON {
		_ = env.PrintJSON(daemonView{Running: false})
	} else {
		fmt.Fprintln(env.Out, "daemon is not running")
	}
	return failure()
}
