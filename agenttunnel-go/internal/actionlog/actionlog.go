// Package actionlog writes a structured, append-only record of everything the
// daemon did — commands, events, agent spawns, RPC calls, tool calls — so a run
// can be debugged after the fact. The log is JSON Lines, one action per line,
// under <state>/logs/actions.jsonl.
package actionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Func is the logging signature handed to components, so they depend on a shape
// rather than on this package. A nil Func is safe to call.
type Func func(kind string, fields map[string]any)

const defaultMaxBytes = 32 << 20

// Logger appends actions to one file, rotating to a single ".1" backup.
type Logger struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	maxBytes int64
}

// Open appends to (or creates) the log at path.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{path: path, file: f, maxBytes: defaultMaxBytes}, nil
}

// Path is the file actions are written to.
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Log records one action. It is safe on a nil Logger.
func (l *Logger) Log(kind string, fields map[string]any) {
	if l == nil {
		return
	}
	record := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		record[k] = v
	}
	record["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	record["kind"] = kind

	data, err := json.Marshal(record)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		return
	}
	l.rotateLocked()
}

// Func returns the logger as a plain function value.
func (l *Logger) Func() Func {
	if l == nil {
		return nil
	}
	return l.Log
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *Logger) rotateLocked() {
	if l.maxBytes <= 0 {
		return
	}
	info, err := l.file.Stat()
	if err != nil || info.Size() < l.maxBytes {
		return
	}
	_ = l.file.Close()
	_ = os.Rename(l.path, l.path+".1")
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		l.file = nil
		return
	}
	l.file = f
}
