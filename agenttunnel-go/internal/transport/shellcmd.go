package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// shellRun describes one `! command` execution from the composer.
type shellRun struct {
	Dir      string
	Command  string
	MaxBytes int           // combined stdout+stderr kept; the rest is dropped
	Timeout  time.Duration // the command is killed when it runs longer
	// Started, when set, is called once the child process is running. Tests use
	// it to cancel a command without sleeping.
	Started func()
}

const (
	// shellOutputCap bounds one command's durable event. Every command is a
	// message in the thread log, so the cap also bounds log growth.
	shellOutputCap = 64 << 10
	shellTimeout   = 120 * time.Second
)

// shellMessage renders a finished command as one durable message: the command
// and its output in a fence, then a status line. The fence grows past any
// backtick run in the body so output cannot break out of it.
func shellMessage(command string, res shellResult) string {
	body := "$ " + command + "\n" + strings.TrimRight(res.Output, "\n")
	fence := "```"
	for strings.Contains(body, fence) {
		fence += "`"
	}
	var status string
	switch {
	case res.Stopped:
		status = "stopped"
	case res.TimedOut:
		status = fmt.Sprintf("timed out after %s", shellTimeout)
	default:
		status = fmt.Sprintf("exit %d", res.ExitCode)
	}
	if res.Truncated {
		status += " · output truncated"
	}
	return fence + "sh\n" + body + "\n" + fence + "\n" + status
}

// shellResult is what a finished command reports back to the thread.
type shellResult struct {
	Output    string
	ExitCode  int // -1 when the process was killed
	Truncated bool
	TimedOut  bool
	Stopped   bool // ctx was cancelled (the Stop button or a dropped client)
}

// runShell runs a command through the shell with stdin closed, merging stdout
// and stderr into one capped buffer. The child inherits the daemon environment
// minus AT_* variables, so an agent's tool token never reaches a user command.
func runShell(ctx context.Context, run shellRun) (shellResult, error) {
	ctx, cancel := context.WithTimeout(ctx, run.Timeout)
	defer cancel()

	name, args := shellInvocation(run.Command)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = run.Dir
	cmd.Env = childEnv()
	// A grandchild that keeps the pipe open must not hang Wait.
	cmd.WaitDelay = time.Second
	configureShell(cmd)

	out := &cappedBuffer{max: run.MaxBytes}
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		return shellResult{}, fmt.Errorf("start command: %w", err)
	}
	if run.Started != nil {
		run.Started()
	}
	waitErr := cmd.Wait()

	// A non-zero exit or a kill arrives as *exec.ExitError; those are results,
	// not failures. ErrWaitDelay means a grandchild kept the pipe open after the
	// shell exited, and the output we have is still the command's output.
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return shellResult{}, fmt.Errorf("run command: %w", waitErr)
	}
	return shellResult{
		Output:    out.String(),
		ExitCode:  cmd.ProcessState.ExitCode(),
		Truncated: out.truncated,
		TimedOut:  errors.Is(ctx.Err(), context.DeadlineExceeded),
		Stopped:   errors.Is(ctx.Err(), context.Canceled),
	}, nil
}

// cappedBuffer keeps the first max bytes written and records whether it
// dropped any. Stdout and Stderr share one writer, so exec serialises writes.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.max - b.buf.Len()
	switch {
	case room >= len(p):
		b.buf.Write(p)
	case room > 0:
		b.buf.Write(p[:room])
		b.truncated = true
	default:
		b.truncated = b.truncated || len(p) > 0
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }

// childEnv is the daemon environment without AT_* variables: the agent tool
// token and agent/thread ids belong to agents, not to user commands.
func childEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "AT_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
