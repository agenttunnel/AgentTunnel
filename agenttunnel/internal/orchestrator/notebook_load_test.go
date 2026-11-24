//go:build !windows

package orchestrator

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contextstore"
)

// A thread whose notebook load is stuck must not hold up other threads. The
// stuck load is a FIFO: opening it for reading blocks until a writer opens it,
// so the load is parked on the disk with no sleep. The guard timeouts only fire
// when the code is wrong.
func TestSlowNotebookLoadDoesNotBlockOtherThreads(t *testing.T) {
	dir := t.TempDir()
	r, _, _ := newPersistRunner(t, dir, `true`)
	fifo := filepath.Join(dir, "tslow.jsonl")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
				w.Close()
			}
		})
	}
	t.Cleanup(release)

	loading := make(chan *contextstore.Notebook, 1)
	go func() { loading <- r.notebookFor("tslow") }()

	other := make(chan *contextstore.Notebook, 1)
	go func() { other <- r.notebookFor("t1") }()
	select {
	case nb := <-other:
		if nb != r.notebookFor("t1") {
			t.Fatal("thread t1 got a different notebook on the second lookup")
		}
	case <-time.After(10 * time.Second):
		release()
		t.Fatal("thread t1 waited behind thread tslow's load")
	}

	dropped := make(chan struct{})
	go func() {
		r.DropNotebook("t1")
		close(dropped)
	}()
	select {
	case <-dropped:
	case <-time.After(10 * time.Second):
		release()
		t.Fatal("dropping thread t1 waited behind thread tslow's load")
	}

	release()
	select {
	case <-loading:
	case <-time.After(10 * time.Second):
		t.Fatal("thread tslow's load never finished")
	}
}
