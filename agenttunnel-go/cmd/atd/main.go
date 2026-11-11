// Command atd is the AgentTunnel daemon: it runs the orchestration server and
// serves the HTTP/WebSocket API that clients (desktop, web, mobile) connect to.
//
// `atd tool …` is a second mode: a callable tool an agent runs from its shell to
// dispatch work or report back to the orchestrator.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolcli"
	"github.com/agenttunnel/agenttunnel-go/pkg/daemon"
)

// version is the release version, set at link time by the release workflow
// (-ldflags "-X main.version=..."). Local builds report "dev".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "tool" {
		os.Exit(toolcli.Run(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("atd", version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "update" {
		os.Exit(runUpdate(os.Args[2:]))
	}
	// The agent environment is exported to child agents, so a stray invocation
	// from inside a session must not start another daemon.
	if os.Getenv("AT_AGENT_ID") != "" {
		fmt.Fprintln(os.Stderr, "atd: refusing to start a daemon inside an agent session")
		os.Exit(2)
	}

	host := flag.String("host", "127.0.0.1", "interface to bind")
	port := flag.Int("port", 7331, "port to bind")
	homeDir := flag.String("home-dir", defaultStateDir(), "state directory")
	token := flag.String("token", "", "auth token (generated when empty)")
	workdir := flag.String("workdir", ".", "working directory handed to agents")
	maxPasses := flag.Int("max-passes", 3, "orchestrator passes per turn")
	concurrency := flag.Int("concurrency", 4, "max concurrent subagents")
	relayURL := flag.String("relay", "", "enable the outbound relay at this wss:// (or loopback ws://) URL")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tok := *token
	if tok == "" {
		tok = mustToken()
	}

	daemon.EnrichPATH()

	d, err := daemon.New(ctx, daemon.Options{
		StateDir: *homeDir,
		Token:    tok,
		Workdir:  *workdir,
		Config: orchestrator.Config{
			MaxPasses:      *maxPasses,
			MaxConcurrency: *concurrency,
		},
		Version:    version,
		OnShutdown: stop,
	})
	if err != nil {
		log.Fatalf("atd: %v", err)
	}
	defer d.Close()
	if *relayURL != "" {
		// Saved to settings like a desktop enable, so it persists across restarts.
		if err := d.Relay.Enable(*relayURL); err != nil {
			log.Fatalf("atd: --relay: %v", err)
		}
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(*host, fmt.Sprint(*port)))
	if err != nil {
		log.Fatalf("atd: %v", err)
	}
	// Agents call the tool endpoint at this address.
	d.SetEndpoint("http://" + ln.Addr().String())

	// Publish how to reach this daemon, now that the port is known. A CLI
	// (`at`) reads it to attach or stop; the file is removed on shutdown so a
	// stale entry cannot outlive the process.
	if err := discovery.Write(*homeDir, discovery.Info{
		WSURL:     "ws://" + ln.Addr().String() + "/ws",
		HTTPURL:   "http://" + ln.Addr().String(),
		Token:     tok,
		PID:       os.Getpid(),
		Version:   version,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		log.Fatalf("atd: write %s: %v", discovery.FileName, err)
	}
	defer discovery.Remove(*homeDir)

	srv := &http.Server{
		Handler:           d.Server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("atd listening on http://%s", ln.Addr())
	if isLoopbackListener(ln) {
		log.Printf("pairing URL: http://%s/?token=%s", ln.Addr(), tok)
	} else {
		// The token stays out of logs off loopback. Set --token and send it as
		// an Authorization: Bearer header.
		log.Printf("atd is bound off loopback; pairing URL omitted so the token stays out of logs (set --token and send Authorization: Bearer)")
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("atd: %v", err)
	}
}

func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agenttunnel"
	}
	return filepath.Join(home, ".agenttunnel")
}

// isLoopbackListener reports whether the listener is bound to loopback only. It
// checks the address the kernel actually bound, so a host name like "localhost"
// is judged by its resolved IP.
func isLoopbackListener(ln net.Listener) bool {
	addr, ok := ln.Addr().(*net.TCPAddr)
	return ok && addr.IP.IsLoopback()
}

func mustToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		log.Fatalf("atd: generate token: %v", err)
	}
	return hex.EncodeToString(b[:])
}
