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

	"github.com/agenttunnel/agenttunnel-go/internal/orchestrator"
	"github.com/agenttunnel/agenttunnel-go/internal/toolcli"
	"github.com/agenttunnel/agenttunnel-go/pkg/daemon"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "tool" {
		os.Exit(toolcli.Run(os.Args[2:]))
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
	})
	if err != nil {
		log.Fatalf("atd: %v", err)
	}
	defer d.Close()

	ln, err := net.Listen("tcp", net.JoinHostPort(*host, fmt.Sprint(*port)))
	if err != nil {
		log.Fatalf("atd: %v", err)
	}
	// Agents call the tool endpoint at this address.
	d.SetEndpoint("http://" + ln.Addr().String())

	srv := &http.Server{
		Handler:           d.Server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("atd listening on http://%s", ln.Addr())
	log.Printf("pairing URL: http://%s/?token=%s", ln.Addr(), tok)

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

func mustToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		log.Fatalf("atd: generate token: %v", err)
	}
	return hex.EncodeToString(b[:])
}
