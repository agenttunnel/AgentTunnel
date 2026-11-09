package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/agenttunnel/agenttunnel-go/internal/update"
)

// runUpdate implements `atd update`. It reports the newest release for this
// install and, after confirmation, installs it. A running daemon keeps the old
// build until it restarts.
func runUpdate(args []string) int {
	fs := flag.NewFlagSet("atd update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "report whether an update is available and exit")
	assumeYes := fs.Bool("y", false, "install without prompting")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Agents run inside this binary's install tree; an agent must not be able
	// to replace the binaries it runs from.
	if os.Getenv("AT_AGENT_ID") != "" {
		fmt.Fprintln(os.Stderr, "atd update: refusing to update from inside an agent session")
		return 2
	}

	u, err := update.New(version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atd update:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := u.Check(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atd update:", err)
		return 1
	}
	switch {
	case !st.Enabled:
		fmt.Println("atd: this is a development build; updates are disabled")
		return 0
	case st.Latest == "":
		fmt.Println("atd: no release has a build for this platform")
		return 0
	case !st.Available:
		fmt.Printf("atd %s is up to date\n", st.Current)
		return 0
	}
	fmt.Printf("update available: %s -> %s\n", st.Current, st.Latest)
	if *checkOnly {
		return 0
	}
	if !*assumeYes && !confirm(fmt.Sprintf("install %s? [y/N] ", st.Latest)) {
		fmt.Println("not installed")
		return 0
	}

	st, err = u.Apply(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "atd update:", err)
		return 1
	}
	fmt.Printf("installed %s; restart atd to run it\n", st.Installed)
	return 0
}

// confirm reads one answer from stdin. Anything but yes, including end of
// input, counts as no.
func confirm(question string) bool {
	fmt.Print(question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
