// Command agenttunnel is the desktop client. It embeds the AgentTunnel daemon,
// serves it on a loopback port, and opens a native window (system webview) onto
// the UI. The UI is just another client: it talks to the daemon over the same
// WebSocket API that web and mobile use.
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
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolcli"
	"github.com/agenttunnel/agenttunnel-go/pkg/daemon"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is the release version, set at link time by the release workflow
// (-ldflags "-X main.version=..."). Local builds report "dev".
var version = "dev"

func main() {
	// Arguments mean someone invoked this binary as a CLI. `tool` is the
	// agent-callable subcommand; a single directory argument opens a thread in
	// that project (forwarded to the running app when there is one); anything
	// else is a usage error — never boot a second window.
	dir := ""
	if args := os.Args[1:]; len(args) > 0 {
		switch {
		case args[0] == "tool":
			os.Exit(toolcli.Run(args[1:]))
		case args[0] == "-h" || args[0] == "--help":
			usage()
			os.Exit(0)
		case args[0] == "--version":
			fmt.Println("agenttunnel", version)
			os.Exit(0)
		case len(args) == 1:
			d, err := launchDirArg(args[0])
			if err != nil {
				fmt.Fprintln(os.Stderr, "agenttunnel:", err)
				os.Exit(2)
			}
			dir = d
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
	if forwardToRunning(state, dir) {
		return
	}

	cwd, _ := os.Getwd()
	d, err := daemon.New(ctx, daemon.Options{
		StateDir: state,
		Token:    mustToken(),
		Workdir:  cwd,
		Config:   orchestrator.Config{MaxPasses: 3, MaxConcurrency: 4},
	})
	if err != nil {
		log.Fatalf("agenttunnel: %v", err)
	}
	defer d.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("agenttunnel: %v", err)
	}
	// Agents call the tool endpoint at this address.
	d.SetEndpoint("http://" + ln.Addr().String())

	wsURL := fmt.Sprintf("ws://%s/ws", ln.Addr().String())
	if err := writeLock(state, launchLock{WsURL: wsURL, Token: d.Token}); err != nil {
		log.Fatalf("agenttunnel: write launch lock: %v", err)
	}
	defer removeLock(state)

	srv := &http.Server{Handler: d.Server.Handler()}
	go func() { _ = srv.Serve(ln) }()

	conn := map[string]any{
		"wsUrl":     wsURL,
		"httpUrl":   fmt.Sprintf("http://%s", ln.Addr().String()),
		"token":     d.Token,
		"launchDir": dir,
	}

	app := application.New(application.Options{
		Name:        "AgentTunnel",
		Description: "Multi-agent coding workflow",
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
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

	// Hand connection info to the UI. The UI asks for it on mount; the startup
	// burst covers the window before the webview is listening.
	app.Event.On("at:request", func(*application.CustomEvent) {
		app.Event.Emit("at:connection", conn)
	})

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
	go func() {
		for i := 0; i < 40; i++ {
			app.Event.Emit("at:connection", conn)
			time.Sleep(250 * time.Millisecond)
		}
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
	_ = srv.Shutdown(context.Background())
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agenttunnel            start the app")
	fmt.Fprintln(os.Stderr, "       agenttunnel <dir>      open a thread in that project (creates the project if unknown)")
	fmt.Fprintln(os.Stderr, "       agenttunnel tool ...   agent-callable tool")
	fmt.Fprintln(os.Stderr, "       agenttunnel --version  print the release version")
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
		log.Fatalf("agenttunnel: generate token: %v", err)
	}
	return hex.EncodeToString(b[:])
}
