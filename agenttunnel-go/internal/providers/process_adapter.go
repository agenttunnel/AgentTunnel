package providers

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ProcessAdapter runs a provider CLI as a child process and translates its
// stdout/stderr lines into normalized Events. Every CLI-shaped harness uses it.
type ProcessAdapter struct {
	driver Driver

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	events   chan Event
	stop     chan struct{}
	waitDone chan struct{}
	waitErr  error
	closed   bool
}

// NewProcessAdapter builds an adapter for a driver.
func NewProcessAdapter(driver Driver) *ProcessAdapter {
	return &ProcessAdapter{
		driver:   driver,
		events:   make(chan Event, 256),
		stop:     make(chan struct{}),
		waitDone: make(chan struct{}),
	}
}

func (p *ProcessAdapter) Harness() contracts.Harness { return p.driver.Harness() }

// Start launches the harness process and begins streaming. It returns once the
// process is started; the events channel closes when the process exits.
func (p *ProcessAdapter) Start(ctx context.Context, spec SessionSpec) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return errors.New("adapter already started")
	}

	inv, tr, err := p.driver.BuildInvocation(spec)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, inv.Binary, inv.Args...)
	if spec.Workdir != "" {
		cmd.Dir = spec.Workdir
	}
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}

	// Only open a stdin pipe when the prompt is delivered that way. When the
	// prompt is an argument, stdin is left closed (the child gets /dev/null):
	// several harness CLIs read stdin whenever it is not a TTY and would
	// otherwise block forever waiting for EOF.
	var stdin io.WriteCloser
	if inv.PromptVia == "stdin" {
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return err
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	p.stdin = stdin

	if inv.PromptVia == "stdin" && stdin != nil && spec.Prompt != "" {
		_, _ = io.WriteString(stdin, spec.Prompt)
		if !strings.HasSuffix(spec.Prompt, "\n") {
			_, _ = io.WriteString(stdin, "\n")
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.pump(stdout, tr, contracts.LogText) }()
	go func() { defer wg.Done(); p.pump(stderr, tr, contracts.LogRaw) }()

	go func() {
		wg.Wait() // drain pipes fully before reaping
		err := cmd.Wait()
		if err != nil {
			p.emit(Event{Kind: contracts.LogError, Text: err.Error()})
		}
		p.finish(err)
	}()
	return nil
}

// Stream returns the normalized event channel for the running session.
func (p *ProcessAdapter) Stream(_ context.Context) (<-chan Event, error) {
	p.mu.Lock()
	started := p.cmd != nil
	p.mu.Unlock()
	if !started {
		return nil, errors.New("adapter not started")
	}
	return p.events, nil
}

// Send writes a follow-up message to the harness's stdin.
func (p *ProcessAdapter) Send(_ context.Context, message string) error {
	p.mu.Lock()
	stdin := p.stdin
	p.mu.Unlock()
	if stdin == nil {
		return errors.New("stdin is closed")
	}
	_, err := io.WriteString(stdin, message+"\n")
	return err
}

// Interrupt asks the harness to stop the current turn.
func (p *ProcessAdapter) Interrupt(_ context.Context) error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return errors.New("process is not running")
	}
	return cmd.Process.Signal(os.Interrupt)
}

// Stop terminates the harness process.
func (p *ProcessAdapter) Stop(_ context.Context) error {
	p.mu.Lock()
	cmd := p.cmd
	p.stdin = nil
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// Wait blocks until the process has exited and returns its exit error.
func (p *ProcessAdapter) Wait(ctx context.Context) error {
	select {
	case <-p.waitDone:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *ProcessAdapter) pump(r io.Reader, tr Translator, kind contracts.LogKind) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if tr == nil {
			p.emit(Event{Kind: kind, Text: line})
			continue
		}
		for _, ev := range tr.Translate(line) {
			p.emit(ev)
		}
	}
}

func (p *ProcessAdapter) emit(ev Event) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	ch := p.events
	p.mu.Unlock()
	select {
	case ch <- ev:
	case <-p.stop:
	}
}

func (p *ProcessAdapter) finish(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.waitErr = err
	close(p.stop)
	close(p.events)
	close(p.waitDone)
}
