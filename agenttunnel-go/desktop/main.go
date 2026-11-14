// Command agenttunnel-desktop is the desktop client. It either embeds the AgentTunnel
// daemon on a loopback port, or attaches to a daemon another process owns
// (started by the `agenttunnel` CLI or left running in the background), and opens a
// native window (system webview) onto the UI. The UI is just another client: it
// talks to the daemon over the same WebSocket API that web and mobile use.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolcli"
	"github.com/agenttunnel/agenttunnel-go/pkg/daemon"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is the release version, set at link time by the release workflow
// (-ldflags "-X main.version=..."). Local builds report "dev".
var version = "dev"

// keepRunning mirrors desktop-prefs.json: when the window closes, a
// desktop-owned daemon keeps serving instead of exiting with the app.
var keepRunning atomic.Bool

func main() {
	// Arguments mean someone invoked this binary as a CLI. `tool` is the
	// agent-callable subcommand; a single directory argument opens a thread in
	// that project (forwarded to the running app when there is one); anything
	// else is a usage error — never boot a second window.
	dir, thread := "", ""
	if args := os.Args[1:]; len(args) > 0 {
		switch {
		case args[0] == "tool":
			os.Exit(toolcli.Run(args[1:]))
		case args[0] == "-h" || args[0] == "--help":
			usage()
			os.Exit(0)
		case args[0] == "--version":
			fmt.Println("agenttunnel-desktop", version)
			os.Exit(0)
		case len(args) == 1:
			d, err := launchDirArg(args[0])
			if err != nil {
				fmt.Fprintln(os.Stderr, "agenttunnel-desktop:", err)
				os.Exit(2)
			}
			dir = d
		case args[0] == "--thread" && len(args) == 2 && strings.TrimSpace(args[1]) != "":
			thread = strings.TrimSpace(args[1])
		default:
			usage()
			os.Exit(2)
		}
	}
	// Defence in depth: the agent environment is exported to child agents, so a
	// stray invocation from inside a session must not start the app either.
	if os.Getenv("AT_AGENT_ID") != "" {
		fmt.Fprintln(os.Stderr, "refusing to start the app inside an agent session")
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemon.EnrichPATH()

	state := stateDir()
	// An instance is already running: hand the request to it and stay out of
	// the way — two daemons must never share one state directory.
	if forwardToRunning(state, dir, thread) {
		return
	}

	prefsNow := loadPrefs(state)
	keepRunning.Store(prefsNow.KeepRunning)

	// A saved remote daemon takes the window. No local daemon is started, and
	// a local one never shadows it.
	if prefsNow.Remote != nil {
		runRemote(state, *prefsNow.Remote, dir, thread)
		return
	}

	// A daemon someone else owns (an `agenttunnel` autostart, or a desktop left in the
	// background) answers discovery. Attach to it: never start a second daemon
	// and never touch its state files.
	if target, ok := findDaemon(state); ok {
		runAttached(state, target, dir, thread)
		return
	}

	runOwned(ctx, cancel, state, dir, thread)
}

// runAttached shows a window against a daemon this process does not own. The
// daemon (and its discovery files) belongs to whoever started it, so this path
// writes nothing and removes nothing on the way out.
func runAttached(state string, target daemonTarget, dir, thread string) {
	conn := shellConn{
		wsURL:        target.wsURL,
		httpURL:      target.httpURL,
		token:        target.token,
		launchDir:    dir,
		launchThread: thread,
		host:         target.host,
		attached:     true,
	}
	// Closing the window ends this shell; the external daemon keeps running.
	app := newApp(&connHolder{local: &conn}, state, true, nil)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// runOwned starts the embedded daemon and shows a window onto it.
func runOwned(ctx context.Context, cancel context.CancelFunc, state, dir, thread string) {
	cwd, _ := os.Getwd()
	var appRef atomic.Pointer[application.App]
	var windowClosed atomic.Bool
	var terminating atomic.Bool

	d, err := daemon.New(ctx, daemon.Options{
		StateDir: state,
		Token:    mustToken(),
		Workdir:  cwd,
		Config:   orchestrator.Config{MaxPasses: 3, MaxConcurrency: 4},
		Version:  version,
		// `agenttunnel daemon stop` must take the window down with the daemon.
		OnShutdown: func() {
			cancel()
			if a := appRef.Load(); a != nil {
				a.Quit()
			}
		},
	})
	if err != nil {
		log.Fatalf("agenttunnel-desktop: %v", err)
	}
	defer d.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("agenttunnel-desktop: %v", err)
	}
	// Agents call the tool endpoint at this address.
	d.SetEndpoint("http://" + ln.Addr().String())

	// Serve before publishing the lock: a second window that reads
	// daemon.json immediately must find a daemon that answers.
	srv := &http.Server{Handler: d.Server.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = srv.Shutdown(shutdownCtx)
	}()

	wsURL := fmt.Sprintf("ws://%s/ws", ln.Addr().String())
	if err := writeLock(state, launchLock{WsURL: wsURL, Token: d.Token}); err != nil {
		log.Fatalf("agenttunnel-desktop: write launch lock: %v", err)
	}
	defer removeLock(state)

	conn := shellConn{
		wsURL:        wsURL,
		httpURL:      fmt.Sprintf("http://%s", ln.Addr().String()),
		token:        d.Token,
		launchDir:    dir,
		launchThread: thread,
		host:         ln.Addr().String(),
	}
	app := newApp(&connHolder{local: &conn}, state, false, func(a *application.App) {
		windowClosed.Store(true)
		if keepRunning.Load() {
			// The daemon stays; drop this window's launch lock so a fresh
			// `agenttunnel` attaches to the daemon instead of forwarding into a
			// window that no longer exists. daemon.json stays for `agenttunnel`.
			_ = os.Remove(lockPath(state))
			return
		}
		go a.Quit()
	})
	appRef.Store(app)
	// Cmd+Q terminates the app outright; only a bare window close is the
	// "leave it running" case, so remember the difference.
	app.Event.OnApplicationEvent(events.Mac.ApplicationWillTerminate, func(*application.ApplicationEvent) {
		terminating.Store(true)
	})

	// SIGINT/SIGTERM always stop the daemon, including when keep-running left
	// it serving headless after the window closed.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sig)
		<-sig
		cancel()
		app.Quit()
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
	// Some platforms end the run loop when the last window closes even with
	// ApplicationShouldTerminateAfterLastWindowClosed off. Honour keep-running
	// there too: the daemon keeps serving until a signal or daemon.shutdown.
	if keepRunning.Load() && windowClosed.Load() && !terminating.Load() {
		<-ctx.Done()
	}
}

// runRemote shows a window onto the remote daemon saved in prefs. Nothing is
// started locally; project-opening arguments need the local daemon, so they are
// ignored with a note.
func runRemote(state string, cfg remoteConfig, dir, thread string) {
	if dir != "" || thread != "" {
		fmt.Fprintln(os.Stderr, "agenttunnel-desktop: a remote daemon is configured; opening a project here needs the local daemon, so the argument is ignored")
	}
	proxy, err := startRemoteProxy(cfg)
	if err != nil {
		log.Fatalf("agenttunnel-desktop: remote daemon: %v", err)
	}
	holder := &connHolder{}
	holder.setRemote(proxy)
	app := newApp(holder, state, true, nil)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// shellConn is the connection handoff the UI receives from the shell. It is
// only shell capability data (where the daemon lives and how it is hosted);
// everything else rides the same daemon WebSocket as any other client.
type shellConn struct {
	wsURL        string
	httpURL      string
	token        string
	launchDir    string
	launchThread string
	host         string
	attached     bool
	// remoteName and remoteURL are set while the window drives a remote daemon.
	// The remote token is never part of the handoff.
	remoteName string
	remoteURL  string
}

func (c shellConn) payload() map[string]any {
	out := map[string]any{
		"wsUrl":        c.wsURL,
		"httpUrl":      c.httpURL,
		"token":        c.token,
		"launchDir":    c.launchDir,
		"launchThread": c.launchThread,
		"host":         c.host,
		"attached":     c.attached,
		"keepRunning":  keepRunning.Load(),
	}
	if c.remoteURL != "" {
		out["remote"] = map[string]any{"name": c.remoteName, "url": c.remoteURL}
	}
	return out
}

// newApp creates the window and wires the shell's capability events. A nil
// onWindowClosing leaves the platform's close behavior alone (attached mode).
func newApp(holder *connHolder, state string, terminateOnClose bool, onWindowClosing func(*application.App)) *application.App {
	app := application.New(application.Options{
		Name:        "AgentTunnel",
		Description: "Multi-agent coding workflow",
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: terminateOnClose,
		},
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "AgentTunnel",
		Width:            1320,
		Height:           860,
		MinWidth:         960,
		MinHeight:        620,
		BackgroundColour: application.NewRGB(9, 10, 16),
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 44,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		URL: "/",
	})
	if onWindowClosing != nil {
		win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			onWindowClosing(app)
		})
	}

	installShellEvents(app, holder, state)
	return app
}

// installShellEvents wires the events only the native shell can answer:
// connection info, the native folder picker, external links, and the
// keep-running preference.
func installShellEvents(app *application.App, holder *connHolder, state string) {
	// Hand connection info to the UI. The UI asks for it on mount; the startup
	// burst covers the window before the webview is listening.
	app.Event.On("at:request", func(*application.CustomEvent) {
		app.Event.Emit("at:connection", holder.current().payload())
	})
	installRemoteEvents(app, holder, state)

	// Native folder picker, used when adding a project. The UI asks over an event
	// and receives the chosen path (empty when cancelled).
	app.Event.On("at:pick-folder", func(*application.CustomEvent) {
		go func() {
			path, err := app.Dialog.OpenFile().
				SetTitle("Select a project folder").
				CanChooseFiles(false).
				CanChooseDirectories(true).
				PromptForSingleSelection()
			if err != nil {
				app.Event.Emit("at:folder", map[string]any{"path": "", "error": err.Error()})
				return
			}
			app.Event.Emit("at:folder", map[string]any{"path": path})
		}()
	})
	// Open a link in the user's default browser. Only web links pass, so page
	// content can never launch a local file or another app.
	app.Event.On("at:open-url", func(e *application.CustomEvent) {
		data, _ := e.Data.(map[string]any)
		raw, _ := data["url"].(string)
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return
		}
		_ = app.Browser.OpenURL(u.String())
	})
	// Persist the keep-running preference. The flag lives in the shell, so the
	// decision to outlive the window is made here rather than in the daemon.
	app.Event.On("at:keep-running", func(e *application.CustomEvent) {
		data, _ := e.Data.(map[string]any)
		enabled, _ := data["enabled"].(bool)
		keepRunning.Store(enabled)
		if err := updatePrefs(state, func(p *prefs) { p.KeepRunning = enabled }); err != nil {
			log.Printf("agenttunnel-desktop: save desktop preferences: %v", err)
		}
	})

	go func() {
		for i := 0; i < 40; i++ {
			app.Event.Emit("at:connection", holder.current().payload())
			time.Sleep(250 * time.Millisecond)
		}
	}()
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agenttunnel-desktop            start the app")
	fmt.Fprintln(os.Stderr, "       agenttunnel-desktop <dir>      open a thread in that project (creates the project if unknown)")
	fmt.Fprintln(os.Stderr, "       agenttunnel-desktop --thread <id>  show an existing thread")
	fmt.Fprintln(os.Stderr, "       agenttunnel-desktop tool ...   agent-callable tool")
	fmt.Fprintln(os.Stderr, "       agenttunnel-desktop --version  print the release version")
}

func stateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agenttunnel"
	}
	return filepath.Join(home, ".agenttunnel")
}

func mustToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		log.Fatalf("agenttunnel-desktop: generate token: %v", err)
	}
	return hex.EncodeToString(b[:])
}
