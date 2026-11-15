// Command at is the AgentTunnel command-line client. It talks to the same
// daemon the desktop app embeds and starts one on demand.
package main

import (
	"os"

	"github.com/agenttunnel/agenttunnel-go/internal/cli"
)

// version is the release version, set at link time by the release workflow
// (-ldflags "-X main.version=..."). Local builds report "dev".
var version = "dev"

func main() {
	os.Exit(cli.Main(os.Args[1:], version))
}
