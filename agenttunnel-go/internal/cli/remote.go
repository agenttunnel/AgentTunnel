package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

func init() {
	Register(Command{
		Name:    "remote",
		Summary: "Save and use remote daemons",
		Subcommands: []Command{
			{
				Name:    "add",
				Summary: "Save a remote daemon profile",
				Usage:   "agenttunnel remote add <name> <wss-url> [--token-stdin]",
				Run:     runRemoteAdd,
			},
			{
				Name:    "use",
				Summary: "Target a saved remote daemon by default",
				Usage:   "agenttunnel remote use <name>",
				Run:     runRemoteUse,
			},
			{
				Name:    "list",
				Summary: "List saved remote daemons",
				Usage:   "agenttunnel remote list",
				Run:     runRemoteList,
			},
			{
				Name:    "rm",
				Summary: "Forget a saved remote daemon",
				Usage:   "agenttunnel remote rm <name>",
				Run:     runRemoteRm,
			},
			{
				Name:    "ping",
				Summary: "Round-trip a remote daemon and report its version and latency",
				Usage:   "agenttunnel remote ping [<name>]",
				Run:     runRemotePing,
			},
		},
	})
}

// remoteNamePattern keeps profile names safe to type and to show in a table.
var remoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// remoteTarget resolves the daemon this invocation talks to without starting a
// local one. Precedence: --daemon, AT_DAEMON_URL, the active saved profile. ok is
// false when none applies and the local daemon should be used.
func (e *Env) remoteTarget() (info discovery.Info, ok bool, err error) {
	if e.DaemonURL != "" {
		return discovery.Info{WSURL: e.DaemonURL, Token: e.flagToken(e.DaemonURL)}, true, nil
	}
	if raw := os.Getenv("AT_DAEMON_URL"); raw != "" {
		wsURL, err := envDaemonURL(raw)
		if err != nil {
			return discovery.Info{}, false, err
		}
		return discovery.Info{WSURL: wsURL, Token: os.Getenv("AT_TOKEN")}, true, nil
	}
	store, err := loadRemotes()
	if err != nil {
		return discovery.Info{}, false, err
	}
	p, ok := store.active()
	if !ok {
		return discovery.Info{}, false, nil
	}
	return discovery.Info{WSURL: p.URL, Token: p.Token}, true, nil
}

// flagToken finds the bearer token for an explicit --daemon URL: AT_TOKEN, then
// the local discovery file when it describes that URL, then a saved profile with
// the same URL.
func (e *Env) flagToken(wsURL string) string {
	if tok := os.Getenv("AT_TOKEN"); tok != "" {
		return tok
	}
	if info, err := discovery.Read(e.StateDir()); err == nil && info.WSURL == wsURL {
		return info.Token
	}
	if store, err := loadRemotes(); err == nil {
		if p, ok := store.byURL(wsURL); ok {
			return p.Token
		}
	}
	return ""
}

// dialTarget connects to a remote daemon and confirms it answers RPCs.
func dialTarget(ctx context.Context, info discovery.Info) (*Client, error) {
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := Dial(dctx, info.WSURL, info.Token)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", info.WSURL, err)
	}
	if err := c.Ping(dctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("%s did not answer: %w", info.WSURL, err)
	}
	return c, nil
}

func runRemoteAdd(_ context.Context, env *Env, args []string) error {
	name, rawURL, tokenStdin, err := parseRemoteAdd(args)
	if err != nil {
		return err
	}
	if err := validateRemoteURL(rawURL); err != nil {
		return err
	}
	var token string
	if tokenStdin {
		if env.In == nil {
			return usageError("--token-stdin needs an input stream")
		}
		data, err := io.ReadAll(env.In)
		if err != nil {
			return fmt.Errorf("read token: %w", err)
		}
		token = strings.TrimSpace(string(data))
		if token == "" {
			return usageError("--token-stdin: stdin was empty")
		}
	}

	store, err := loadRemotes()
	if err != nil {
		return err
	}
	if store.index(name) >= 0 {
		return usageError("remote %q already exists; run `agenttunnel remote rm %s` first", name, name)
	}
	store.Remotes = append(store.Remotes, remoteProfile{Name: name, URL: rawURL, Token: token})
	if err := saveRemotes(store); err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(remoteView{Name: name, URL: rawURL, HasToken: token != ""})
	}
	fmt.Fprintf(env.Out, "saved remote %s (%s); run `agenttunnel remote use %s` to target it\n", name, rawURL, name)
	return nil
}

func parseRemoteAdd(args []string) (name, rawURL string, tokenStdin bool, err error) {
	var positional []string
	for _, arg := range args {
		switch {
		case arg == "--token-stdin":
			tokenStdin = true
		case strings.HasPrefix(arg, "-"):
			return "", "", false, usageError("unknown flag %q (usage: agenttunnel remote add <name> <wss-url> [--token-stdin])", arg)
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) != 2 {
		return "", "", false, usageError("usage: agenttunnel remote add <name> <wss-url> [--token-stdin]")
	}
	name = positional[0]
	if !remoteNamePattern.MatchString(name) {
		return "", "", false, usageError("remote name %q must start with a letter or digit and use only letters, digits, '.', '_' or '-'", name)
	}
	return name, positional[1], tokenStdin, nil
}

func runRemoteUse(_ context.Context, env *Env, args []string) error {
	name, err := oneRemoteName(args, "agenttunnel remote use <name>")
	if err != nil {
		return err
	}
	store, err := loadRemotes()
	if err != nil {
		return err
	}
	i := store.index(name)
	if i < 0 {
		return fmt.Errorf("no saved remote %q (see `agenttunnel remote list`)", name)
	}
	store.Active = name
	if err := saveRemotes(store); err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(remoteView{Name: name, URL: store.Remotes[i].URL, Active: true, HasToken: store.Remotes[i].Token != ""})
	}
	fmt.Fprintf(env.Out, "using remote %s (%s)\n", name, store.Remotes[i].URL)
	return nil
}

// remoteView is what `remote list` and friends print. The token itself is
// never part of it, only whether one is saved.
type remoteView struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Active   bool   `json:"active"`
	HasToken bool   `json:"hasToken"`
}

func runRemoteList(_ context.Context, env *Env, args []string) error {
	if len(args) != 0 {
		return usageError("usage: agenttunnel remote list")
	}
	store, err := loadRemotes()
	if err != nil {
		return err
	}
	views := make([]remoteView, 0, len(store.Remotes))
	for _, p := range store.Remotes {
		views = append(views, remoteView{Name: p.Name, URL: p.URL, Active: p.Name == store.Active, HasToken: p.Token != ""})
	}
	if env.JSON {
		return env.PrintJSON(views)
	}
	if len(views) == 0 {
		fmt.Fprintln(env.Out, "no saved remotes; add one with `agenttunnel remote add <name> <wss-url> --token-stdin`")
		return nil
	}
	rows := make([][]string, 0, len(views))
	for _, v := range views {
		active := ""
		if v.Active {
			active = "*"
		}
		token := "no"
		if v.HasToken {
			token = "yes"
		}
		rows = append(rows, []string{active, v.Name, v.URL, token})
	}
	renderDockerTable(env.Out, []string{"ACTIVE", "NAME", "URL", "TOKEN"}, rows)
	return nil
}

func runRemoteRm(_ context.Context, env *Env, args []string) error {
	name, err := oneRemoteName(args, "agenttunnel remote rm <name>")
	if err != nil {
		return err
	}
	store, err := loadRemotes()
	if err != nil {
		return err
	}
	i := store.index(name)
	if i < 0 {
		return fmt.Errorf("no saved remote %q", name)
	}
	store.Remotes = append(store.Remotes[:i], store.Remotes[i+1:]...)
	if store.Active == name {
		store.Active = ""
	}
	if err := saveRemotes(store); err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(map[string]any{"ok": true, "removed": name})
	}
	fmt.Fprintf(env.Out, "removed remote %s\n", name)
	return nil
}

func oneRemoteName(args []string, usage string) (string, error) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return "", usageError("usage: %s", usage)
	}
	return args[0], nil
}

// runRemotePing dials a remote daemon and times one app.info round trip. With a
// name it pings that profile; without one, it pings whatever --daemon,
// AT_DAEMON_URL or the active profile selects. It never starts a local daemon.
func runRemotePing(ctx context.Context, env *Env, args []string) error {
	if len(args) > 1 || (len(args) == 1 && strings.HasPrefix(args[0], "-")) {
		return usageError("usage: agenttunnel remote ping [<name>]")
	}
	var target discovery.Info
	if len(args) == 1 {
		store, err := loadRemotes()
		if err != nil {
			return err
		}
		i := store.index(args[0])
		if i < 0 {
			return fmt.Errorf("no saved remote %q", args[0])
		}
		target = discovery.Info{WSURL: store.Remotes[i].URL, Token: store.Remotes[i].Token}
	} else {
		info, ok, err := env.remoteTarget()
		if err != nil {
			return err
		}
		if !ok {
			return usageError("no remote daemon selected: pass a name, --daemon <wss-url>, or run `agenttunnel remote use <name>`")
		}
		target = info
	}

	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dialStart := time.Now()
	c, err := Dial(dctx, target.WSURL, target.Token)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", target.WSURL, err)
	}
	defer c.Close()
	dialed := time.Since(dialStart)

	var info struct {
		Version string `json:"version"`
	}
	rpcStart := time.Now()
	if err := c.Call(dctx, "app.info", nil, &info); err != nil {
		return fmt.Errorf("%s did not answer: %w", target.WSURL, err)
	}
	rtt := time.Since(rpcStart)

	if env.JSON {
		return env.PrintJSON(map[string]any{
			"url":         target.WSURL,
			"version":     info.Version,
			"connectMs":   dialed.Milliseconds(),
			"roundTripMs": rtt.Milliseconds(),
			"roundTripUs": rtt.Microseconds(),
		})
	}
	fmt.Fprintf(env.Out, "%s: daemon %s, round trip %s (connect %s)\n", target.WSURL, info.Version, rtt.Round(time.Microsecond), dialed.Round(time.Millisecond))
	return nil
}
