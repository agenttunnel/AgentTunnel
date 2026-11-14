//go:build windows

package cli

import "os"

// tuiResizeSignals has no resize deliveries on Windows: the session re-reads
// the terminal size on every paint, so a resize lands with the next redraw.
func tuiResizeSignals() (<-chan os.Signal, func()) {
	return nil, func() {}
}
