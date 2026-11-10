package scm

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// actionTimeout bounds a write. Push and pull talk to the network, so they get
// more room than a read.
const actionTimeout = 2 * time.Minute

// The actions below are the PR tab's git and forge writes. They never force-push,
// rebase, or reset. Only the client's RPC reaches them, never agent tools: the
// lead alone changes git state (see gitguard).

// Live returns the client that runs the real git and gh CLIs.
func Live() Client {
	return Client{LookPath: exec.LookPath, Run: execRun}
}

// Commit commits the staged changes with message. When all is true it stages
// every change first, the same as `git add -A`.
func (c Client) Commit(ctx context.Context, dir, message string, all bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	message = strings.TrimSpace(message)
	if message == "" {
		return "", errors.New("a commit message is required")
	}
	if all {
		if _, err := c.step(ctx, dir, "git", "add", "-A"); err != nil {
			return "", err
		}
	}
	return c.step(ctx, dir, "git", "commit", "-m", message)
}

// Push publishes the current branch to origin and sets its upstream. It never forces.
func (c Client) Push(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if err := c.onBranch(ctx, dir); err != nil {
		return "", err
	}
	if _, err := c.step(ctx, dir, "git", "push", "-u", "origin", "HEAD"); err != nil {
		return "", err
	}
	return "Pushed", nil
}

// Pull fast-forwards the current branch from its upstream. A branch that cannot
// fast-forward fails instead of merging or rebasing.
func (c Client) Pull(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if err := c.onBranch(ctx, dir); err != nil {
		return "", err
	}
	return c.step(ctx, dir, "git", "pull", "--ff-only")
}

// CreateRequest opens a GitHub pull request for the branch. An empty title lets
// gh build the title and body from the commits. It returns the new PR's URL.
func (c Client) CreateRequest(ctx context.Context, dir, title string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if err := c.githubCLI(ctx, dir); err != nil {
		return "", err
	}
	args := []string{"pr", "create"}
	if title = strings.TrimSpace(title); title != "" {
		args = append(args, "--title", title, "--body", "")
	} else {
		args = append(args, "--fill")
	}
	return c.step(ctx, dir, "gh", args...)
}

// MarkReady moves a draft pull request out of draft.
func (c Client) MarkReady(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if err := c.githubCLI(ctx, dir); err != nil {
		return "", err
	}
	if _, err := c.step(ctx, dir, "gh", "pr", "ready"); err != nil {
		return "", err
	}
	return "Marked ready for review", nil
}

// Merge squash-merges the branch's pull request. gh refuses when the request is
// not mergeable (failing checks, missing review), and that reason is returned.
func (c Client) Merge(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if err := c.githubCLI(ctx, dir); err != nil {
		return "", err
	}
	if _, err := c.step(ctx, dir, "gh", "pr", "merge", "--squash"); err != nil {
		return "", err
	}
	return "Merged", nil
}

// onBranch refuses a detached HEAD. Push and pull have no branch to name there.
func (c Client) onBranch(ctx context.Context, dir string) error {
	out, _, err := c.Run(ctx, dir, "git", "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return errors.New("detached HEAD: check out a branch first")
	}
	return nil
}

// githubCLI checks that origin is GitHub and gh is installed. Pull request
// writes are GitHub-only for now.
func (c Client) githubCLI(ctx context.Context, dir string) error {
	out, _, err := c.Run(ctx, dir, "git", "remote", "get-url", "origin")
	if err != nil {
		return errors.New("no origin remote")
	}
	if providerFor(strings.TrimSpace(string(out))) != "github" {
		return errors.New("pull request actions are GitHub-only for now")
	}
	if _, err := c.LookPath("gh"); err != nil {
		return errors.New("gh is not installed")
	}
	return nil
}

// step runs one CLI. On success it returns the first line of stdout. On failure
// it returns the last line the CLI printed, since git and gh put the reason
// after their progress output ("nothing to commit" comes on stdout, for example).
func (c Client) step(ctx context.Context, dir, name string, args ...string) (string, error) {
	out, stderr, err := c.Run(ctx, dir, name, args...)
	if err != nil {
		reason := lastLine(stderr)
		if reason == "" {
			reason = lastLine(string(out))
		}
		if reason == "" {
			reason = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, args[0], reason)
	}
	return firstLine(string(out), ""), nil
}

// lastLine returns the last non-empty line of text, or "" when there is none.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
