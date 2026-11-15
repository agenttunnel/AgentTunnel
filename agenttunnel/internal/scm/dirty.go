package scm

import (
	"bytes"
	"context"
)

// Dirty reports whether workdir has uncommitted changes, tracked or untracked.
// A git failure reads as clean: the board then places the thread by its PR state.
func Dirty(ctx context.Context, workdir string) bool {
	return Client{Run: execRun}.Dirty(ctx, workdir)
}

// Dirty is Dirty over this client's runner.
func (c Client) Dirty(ctx context.Context, workdir string) bool {
	out, _, err := c.Run(ctx, workdir, "git", "status", "--porcelain")
	if err != nil {
		return false
	}
	return dirtyFromPorcelain(out)
}

// dirtyFromPorcelain reads `git status --porcelain` output: any line means changes.
func dirtyFromPorcelain(out []byte) bool {
	return len(bytes.TrimSpace(out)) > 0
}
