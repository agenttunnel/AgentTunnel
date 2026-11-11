package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/agenttunnel/agenttunnel-go/internal/actionlog"
	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
	"github.com/agenttunnel/agenttunnel-go/internal/logstore"
	"github.com/agenttunnel/agenttunnel-go/internal/models"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
	relayclient "github.com/agenttunnel/agenttunnel-go/internal/relay"
	"github.com/agenttunnel/agenttunnel-go/internal/remote"
	"github.com/agenttunnel/agenttunnel-go/internal/settings"
	"github.com/agenttunnel/agenttunnel-go/internal/steering"
	"github.com/agenttunnel/agenttunnel-go/internal/toolbox"
	"github.com/agenttunnel/agenttunnel-go/internal/transport"
	"github.com/agenttunnel/agenttunnel-go/internal/update"
	"github.com/agenttunnel/agenttunnel-go/internal/usage"
)

// Options configure a daemon.
type Options struct {
	StateDir string
	Token    string
	Workdir  string
	Registry *providers.Registry
	Roles    *contracts.RoleRegistry
	Config   orchestrator.Config
	// ToolPath is the binary agents run for `tool …`. Defaults to this
	// executable; tests point it at a built daemon.
	ToolPath string
	// Version is the release version embedded at link time. Empty or "dev"
	// disables self-update.
	Version string
	// OnShutdown is invoked by the daemon.shutdown RPC to gracefully stop the
	// process that hosts this daemon. The assembly points it at the same
	// signal-cancel path a SIGTERM uses.
	OnShutdown func()
	// RemoteAdapter overrides the shipped WhatsApp adapter. Tests use a fake so
	// they never touch WhatsApp; nil uses NewDefaultAdapter.
	RemoteAdapter remote.Adapter
}

// Daemon is the fully assembled AgentTunnel server: engine, orchestrator, usage,
// logs, and the HTTP/WebSocket surface.
type Daemon struct {
	Engine   *domain.Engine
	Runner   *orchestrator.Runner
	Orch     *orchestrator.Orchestrator
	Server   *transport.Server
	Ledger   *usage.Ledger
	Logs     *logstore.Store
	Settings *settings.Store
	Actions  *actionlog.Logger
	Relay    *relayclient.Client
	Remote   *remote.Bridge
	Token    string

	endpoint *atomic.Value
}

// New assembles a daemon and starts its engine.
func New(ctx context.Context, opts Options) (*Daemon, error) {
	updater, err := update.New(opts.Version)
	if err != nil {
		return nil, err
	}
	if opts.Registry == nil {
		opts.Registry = providers.BuiltinRegistry()
	}
	if opts.Roles == nil {
		opts.Roles = contracts.NewRoleRegistry(contracts.BuiltinRoles()...)
	}

	// Threads are durable: their event logs live under <state>/threads and the
	// read model is rebuilt by replaying them on start. Creation itself is
	// provisional (an unused thread writes nothing); logs written by older
	// versions may still hold creation-only ghosts, so reclaim them here.
	events, err := domain.NewFileStore(filepath.Join(opts.StateDir, "threads"))
	if err != nil {
		return nil, err
	}
	if _, err := events.DropEmptyThreads(); err != nil {
		return nil, err
	}

	// Structured record of everything the daemon does, for debugging a run after
	// the fact. Never written with secrets.
	actions, err := actionlog.Open(filepath.Join(opts.StateDir, "logs", "actions.jsonl"))
	if err != nil {
		return nil, err
	}

	eng := domain.NewEngine(events, opts.Roles)
	eng.SetAudit(actions.Func())
	eng.Start(ctx)
	actions.Log("daemon.start", map[string]any{"stateDir": opts.StateDir, "workdir": opts.Workdir})

	inherited := contextstore.NewInheritedContext(filepath.Join(opts.StateDir, ".orchestrator-context"))
	if err := inherited.Ensure(); err != nil {
		return nil, err
	}
	logs := logstore.New(filepath.Join(opts.StateDir, "logs"))
	ledger := usage.NewLedger()
	mailbox := contextstore.NewMailbox()
	// The runtime box is a drained-once delivery buffer, not the gate: the
	// authoritative switch is the durable thread's peerMailbox state, enforced
	// by the decider. Keeping the runtime box always on also avoids a restart
	// desync where the durable flag persisted but this one reset.
	mailbox.SetEnabled(true)
	store, err := settings.Open(filepath.Join(opts.StateDir, "settings.json"), opts.Workdir)
	if err != nil {
		return nil, err
	}
	modelService := models.NewService(opts.Registry, store)
	harnessUpdates := providers.NewHarnessUpdater(providers.HarnessUpdaterOptions{})

	// The tool inbox, the steering queue, and the address/path agents use.
	box := toolbox.New()
	steerBox := steering.New()
	toolPath := opts.ToolPath
	if toolPath == "" {
		toolPath, _ = os.Executable()
	}
	var endpoint atomic.Value
	endpoint.Store("")

	runner := orchestrator.NewRunner(orchestrator.Deps{
		Engine:    eng,
		Registry:  opts.Registry,
		Roles:     opts.Roles,
		Logs:      logs,
		Ledger:    ledger,
		Mailbox:   mailbox,
		Inherited: inherited,
		Settings:  store,
		Tools:     box,
		Steering:  steerBox,
		Audit:     actions.Func(),
		ToolPath:  toolPath,
		Token:     opts.Token,
		Endpoint: func() string {
			if v, ok := endpoint.Load().(string); ok {
				return v
			}
			return ""
		},
		Cfg: opts.Config,
	})
	orch := orchestrator.New(runner)

	// The relay client is off until enabled in settings. It dials out only and
	// never listens; phone sessions are served by the same server as /ws.
	relay, err := relayclient.New(relayclient.Options{
		StateDir: opts.StateDir,
		Settings: store,
		Logf: func(format string, args ...any) {
			actions.Log("relay", map[string]any{"message": fmt.Sprintf(format, args...)})
		},
	})
	if err != nil {
		return nil, err
	}

	// WhatsApp updates are off until the user connects. The bridge follows the
	// engine so turn results, failures, and questions reach the linked device.
	adapter := opts.RemoteAdapter
	if adapter == nil {
		adapter = remote.NewDefaultAdapter(opts.StateDir)
	}
	var disp *transport.Dispatcher
	// The chat control router needs the dispatcher, and the bridge (which the
	// dispatcher needs) needs the router. Both are closures over this variable.
	var router *transport.ControlRouter
	bridge, err := remote.New(remote.Options{
		StateDir: opts.StateDir,
		Engine:   eng,
		Adapter:  adapter,
		Control: func(ctx context.Context, text string) string {
			if router == nil {
				return ""
			}
			return router.Handle(ctx, text)
		},
		OnChange: func() {
			if disp != nil {
				disp.BroadcastRemoteStatus()
			}
		},
		Logf: func(format string, args ...any) {
			actions.Log("whatsapp", map[string]any{"message": fmt.Sprintf(format, args...)})
		},
	})
	if err != nil {
		return nil, fmt.Errorf("whatsapp bridge: %w", err)
	}

	disp = transport.NewDispatcher(transport.DispatcherDeps{
		Engine:         eng,
		Runner:         runner,
		Orchestrator:   orch,
		Ledger:         ledger,
		Logs:           logs,
		Roles:          opts.Roles,
		Registry:       opts.Registry,
		Settings:       store,
		Models:         modelService,
		Steering:       steerBox,
		Audit:          actions.Func(),
		Updater:        updater,
		Version:        opts.Version,
		HarnessUpdates: harnessUpdates,
		Limits:         providers.NewLimitsProvider(providers.NewClaudeLimits()),
		Relay:          relay,
		BaseCtx:        ctx,
		Shutdown:       opts.OnShutdown,
		Remote:         bridge,
	})
	// Inbound WhatsApp control routes through the daemon's own RPC surface, with
	// a fixed whitelist; see transport.ControlRouter.
	router = transport.NewControlRouter(disp)
	// Questions left open by a previous run have no caller to answer them.
	if err := disp.CloseStaleQuestions(ctx); err != nil {
		return nil, fmt.Errorf("close stale questions: %w", err)
	}

	srv := transport.NewServer(disp, eng, runner, opts.Token, box)
	// Status changes are pushed to every client, so a QR that appears after the
	// connect reply still reaches the window that asked for it.
	disp.SetRemoteNotifier(func(st transport.RemoteStatus) {
		srv.NotifyAll("remote.status.changed", st)
	})
	relay.Bind(srv)
	relay.Start(ctx)
	bridge.Start(ctx)
	// RPC calls broadcast the transitions they cause, but the relay also
	// connects and drops on its own. Follow those moves so the phone row is
	// never stale.
	go followRelayTransitions(ctx, relay, disp)

	return &Daemon{
		Engine:   eng,
		Runner:   runner,
		Orch:     orch,
		Server:   srv,
		Ledger:   ledger,
		Logs:     logs,
		Settings: store,
		Actions:  actions,
		Relay:    relay,
		Remote:   bridge,
		Token:    opts.Token,
		endpoint: &endpoint,
	}, nil
}

// SetEndpoint records the daemon's public base URL so agents can call back.
func (d *Daemon) SetEndpoint(url string) {
	if d.endpoint != nil {
		d.endpoint.Store(url)
	}
}

// followRelayTransitions broadcasts the Remote section whenever the relay
// socket connects or drops in the background. It waits on the client's change
// signals rather than polling, and exits with ctx.
func followRelayTransitions(ctx context.Context, relay *relayclient.Client, disp *transport.Dispatcher) {
	for {
		if err := relay.WaitConnected(ctx); err != nil {
			return
		}
		disp.BroadcastRemoteStatus()
		if err := relay.WaitDisconnected(ctx); err != nil {
			return
		}
		disp.BroadcastRemoteStatus()
	}
}

// Close stops the engine and flushes the action log.
func (d *Daemon) Close() {
	if d.Remote != nil {
		d.Remote.Close()
	}
	if d.Relay != nil {
		d.Relay.Close()
	}
	if d.Actions != nil {
		d.Actions.Log("daemon.stop", nil)
		_ = d.Actions.Close()
	}
	d.Engine.Close()
}

// EnrichPATH appends common tool directories to PATH. An app launched from
// Finder or the Dock inherits a minimal PATH that omits Homebrew and user bin
// directories, so harness CLIs (and their model probes) would otherwise be
// unresolvable. Entrypoints call this before starting the daemon.
func EnrichPATH() {
	home, _ := os.UserHomeDir()
	extra := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".opencode", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".local", "share", "vite-plus", "bin"),
		filepath.Join(home, ".local", "share", "pnpm"),
		filepath.Join(home, "Library", "pnpm"),
		filepath.Join(home, "go", "bin"),
		// vite-plus installs Command Code (commandcode, cmd) here.
		filepath.Join(home, ".local", "share", "vite-plus", "bin"),
	}

	parts := filepath.SplitList(os.Getenv("PATH"))
	have := make(map[string]bool, len(parts))
	for _, p := range parts {
		have[p] = true
	}
	for _, dir := range extra {
		if dir != "" && !have[dir] {
			parts = append(parts, dir)
		}
	}
	_ = os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
}
