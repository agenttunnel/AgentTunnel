// Package logrotate is an io.Writer that keeps a log file bounded. When a write
// would push the active file past the cap, the file moves to <path>.1 (replacing
// the previous backup) and writing continues in a fresh file.
//
// Writes are serialized by a mutex, so goroutines in one process never tear each
// other's lines. Every write also checks that the path still names the file it
// has open, so a second process that rotated the file is followed, not
// written around.
package logrotate

import (
	"errors"
	"os"
	"sync"
)

// DefaultMaxBytes is the cap daemon logs use: 5 MB per file, plus one backup.
const DefaultMaxBytes = 5 << 20

// Writer appends to one file and rotates it at the cap. It is safe for
// concurrent use.
type Writer struct {
	path string
	max  int64
	// rename moves the active file aside; os.Rename in production, a seam in tests.
	rename func(oldpath, newpath string) error
	// retryAbove is the file size at which rotation is attempted again after a
	// failed rename. Zero means rotate as soon as the cap is crossed.
	retryAbove int64

	mu sync.Mutex
	f  *os.File // nil after Close
}

// New opens (creating if needed) the log at path. maxBytes must be positive.
func New(path string, maxBytes int64) (*Writer, error) {
	if maxBytes <= 0 {
		return nil, errors.New("logrotate: max bytes must be positive")
	}
	w := &Writer{path: path, max: maxBytes, rename: os.Rename}
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	w.f = f
	return w, nil
}

// Write appends p. A write to an empty file is never rotated, so a single line
// larger than the cap is kept whole rather than split or dropped.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if err := w.followPath(); err != nil {
		return 0, err
	}
	info, err := w.f.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() > 0 && info.Size()+int64(len(p)) > w.max && info.Size() >= w.retryAbove {
		if err := w.rotate(info.Size()); err != nil {
			return 0, err
		}
	}
	return w.f.Write(p)
}

// Close releases the file. Writes after Close fail with os.ErrClosed.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// followPath reopens the file when the path no longer names the open file: it
// was rotated by another process, or removed. A missing path is recreated.
func (w *Writer) followPath() error {
	open, err := w.f.Stat()
	if err != nil {
		return err
	}
	cur, err := os.Stat(w.path)
	if err == nil && os.SameFile(open, cur) {
		return nil
	}
	_ = w.f.Close()
	w.f = nil
	f, err := openAppend(w.path)
	if err != nil {
		return err
	}
	// A reopened file has its own size; a backoff from the old one does not apply.
	w.retryAbove = 0
	w.f = f
	return nil
}

// rotate moves the active file (currently size bytes) to <path>.1 and reopens
// the path. If the rename fails, the active file keeps growing, since losing
// lines is worse than an unbounded file, and rotation is not retried until the
// file has grown by another cap's worth. Without that backoff a read-only log
// directory would close, rename and reopen on every write.
func (w *Writer) rotate(size int64) error {
	_ = w.f.Close()
	w.f = nil
	if err := w.rename(w.path, w.path+".1"); err != nil {
		w.retryAbove = size + w.max
	} else {
		w.retryAbove = 0
	}
	f, err := openAppend(w.path)
	if err != nil {
		return err
	}
	w.f = f
	return nil
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}
