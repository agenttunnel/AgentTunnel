//go:build windows

package cli

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// enableVirtualTerminal turns on ENABLE_VIRTUAL_TERMINAL_PROCESSING on the
// console behind f, so the SGR and alternate-screen escapes render on legacy
// conhost instead of printing as text. Modern Windows terminals already have
// it on, and a non-console writer fails softly.
func enableVirtualTerminal(f *os.File) error {
	h := syscall.Handle(f.Fd())
	var mode uint32
	r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return fmt.Errorf("GetConsoleMode: not a console")
	}
	const enableVirtualTerminalProcessing = 0x0004
	mode |= enableVirtualTerminalProcessing
	r, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode))
	if r == 0 {
		return fmt.Errorf("SetConsoleMode: %w", err)
	}
	return nil
}

// tuiEnableVT enables VT processing on the output when it is a console. It
// reports whether ANSI escapes are safe to write; the caller falls back to
// the plain theme when it is not, so colors never print as literal text.
func tuiEnableVT(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		// Not a console handle (a test buffer or a redirect): the escape
		// sequences are moot there, and detection picks plain anyway.
		return true
	}
	return enableVirtualTerminal(f) == nil
}

// tuiResizeSignals has no resize deliveries on Windows: the session re-reads
// the terminal size on every paint, so a resize lands with the next redraw.
func tuiResizeSignals() (<-chan os.Signal, func()) {
	return nil, func() {}
}
