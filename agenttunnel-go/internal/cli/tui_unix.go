//go:build !windows

package cli

import (
	"io"
	"os"
	"os/signal"
	"syscall"
)

// tuiResizeSignals reports terminal resizes as they happen.
func tuiResizeSignals() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}

// tuiEnableVT is a no-op off Windows: ANSI handling is the terminal's.
func tuiEnableVT(io.Writer) bool { return true }
