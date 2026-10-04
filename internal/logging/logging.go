// Package logging builds the server's logger: standard output (text or JSON) plus daily text files
// that the admin API can list and serve.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options configures the logger.
type Options struct {
	Level slog.Level
	// JSON selects the format of standard output.
	JSON bool
	// Dir is the folder of the daily files; empty means no file.
	Dir string
	// Retention is how many days of logs are kept (default: 14).
	Retention int
	// Ring also keeps the latest messages in memory; nil turns that off.
	Ring *Ring
}

// ParseLevel converts a level from the configuration.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("invalid log level %q: %w", s, err)
	}
	return l, nil
}

// New returns the logger and something to close the current file.
func New(stdout io.Writer, opts Options) (*slog.Logger, io.Closer, error) {
	handlerOpts := &slog.HandlerOptions{Level: opts.Level}
	var console slog.Handler
	if opts.JSON {
		console = slog.NewJSONHandler(stdout, handlerOpts)
	} else {
		console = slog.NewTextHandler(stdout, handlerOpts)
	}
	handlers := []slog.Handler{console}
	if opts.Ring != nil {
		handlers = append(handlers, opts.Ring.Handler(opts.Level))
	}
	if opts.Dir == "" {
		return slog.New(slog.NewMultiHandler(handlers...)), nopCloser{}, nil
	}
	files, err := NewDailyFile(opts.Dir, opts.Retention)
	if err != nil {
		return nil, nil, err
	}
	handlers = append(handlers, slog.NewTextHandler(files, handlerOpts))
	return slog.New(slog.NewMultiHandler(handlers...)), files, nil
}

// FilePrefix and FileExt make up the log file names: laterna_20260928.log.
const (
	FilePrefix = "laterna_"
	FileExt    = ".log"
)

// DailyFile writes to one file per day and deletes the oldest ones when the day changes.
type DailyFile struct {
	dir       string
	retention int
	now       func() time.Time

	mu   sync.Mutex
	day  string
	file *os.File
}

// NewDailyFile opens today's file in dir.
func NewDailyFile(dir string, retention int) (*DailyFile, error) {
	if retention <= 0 {
		retention = 14
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &DailyFile{dir: dir, retention: retention, now: time.Now}, nil
}

// Write implements io.Writer and switches to a new file when the day changes.
func (d *DailyFile) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	day := d.now().Format("20060102")
	if d.file == nil || day != d.day {
		if err := d.rotate(day); err != nil {
			return 0, err
		}
	}
	return d.file.Write(p)
}

func (d *DailyFile) rotate(day string) error {
	if d.file != nil {
		_ = d.file.Close()
		d.file = nil
	}
	f, err := os.OpenFile(filepath.Join(d.dir, FilePrefix+day+FileExt), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	d.file, d.day = f, day
	d.prune()
	return nil
}

// prune deletes logs past the retention. Dated names sort in order.
func (d *DailyFile) prune() {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasPrefix(n, FilePrefix) && strings.HasSuffix(n, FileExt) {
			logs = append(logs, n)
		}
	}
	slices.Sort(logs)
	for len(logs) > d.retention {
		_ = os.Remove(filepath.Join(d.dir, logs[0]))
		logs = logs[1:]
	}
}

// Close closes the current file.
func (d *DailyFile) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil {
		return nil
	}
	err := d.file.Close()
	d.file = nil
	return err
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
