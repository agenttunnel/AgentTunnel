// Command atrelay is the self-hosted AgentTunnel relay. It routes frames between
// one daemon and its paired phones, so a phone can reach an atd that has no
// inbound port. Run it on a host you control. The relay operator can read the
// traffic it routes, so do not use someone else's relay. See
// docs/relay-protocol.md.
//
// Put a TLS terminator in front of it for public use. Plain listening is for
// loopback or a LAN.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/agenttunnel/agenttunnel-go/pkg/relay"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:7777", "address to listen on (put TLS in front for public use)")
	state := flag.String("state", defaultStatePath(), "file for instance and token hashes (empty keeps state in memory)")
	trustProxy := flag.String("trust-proxy-header", "", "header a TLS proxy you run sets to the client address, e.g. X-Forwarded-For (empty ignores such headers)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := relay.New(relay.Options{
		StatePath:        *state,
		Logf:             log.Printf,
		TrustProxyHeader: *trustProxy,
	})
	if err != nil {
		log.Fatalf("atrelay: %v", err)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("atrelay: %v", err)
	}
	log.Printf("atrelay listening on %s", ln.Addr())
	if err := srv.Serve(ctx, ln); err != nil {
		log.Fatalf("atrelay: %v", err)
	}
}

func defaultStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "atrelay-state.json")
	}
	return filepath.Join(home, ".agenttunnel-relay", "state.json")
}
