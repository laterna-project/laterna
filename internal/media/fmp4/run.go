package fmp4

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/laterna-project/laterna/internal/proc"
)

// ErrStop is returned by a callback to stop the run without an error.
var ErrStop = errors.New("fmp4: stop requested")

// running counts the FFmpeg processes started by Run that have not exited yet.
var running atomic.Int64

// Running returns the number of playback FFmpeg processes currently running (soak tests,
// monitoring).
func Running() int { return int(running.Load()) }

// Command is an FFmpeg command that writes an fMP4 stream to its standard output.
type Command struct {
	// Bin is the executable ("" for ffmpeg from PATH).
	Bin  string
	Args []string
	// Dir is the working directory ("" for the server's). FFmpeg filters find their files there
	// through relative paths, which do not need the escaping a Windows path would.
	Dir string
}

// Run starts FFmpeg and calls onInit once, then onFragment for each fragment, until the source
// ends, ctx is canceled or a callback returns an error (ErrStop stops without an error). A callback
// may block: the pipe fills up and FFmpeg waits without burning CPU. When Run returns, FFmpeg has
// always stopped.
func Run(ctx context.Context, c Command, onInit func([]byte) error, onFragment func(Fragment) error) (err error) {
	bin := c.Bin
	if bin == "" {
		bin = "ffmpeg"
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := proc.Command(ctx, bin, c.Args...)
	cmd.Dir = c.Dir
	stderr := &tail{max: 2048}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("fmp4: starting FFmpeg: %w", err)
	}
	running.Add(1)
	defer running.Add(-1) // after the Wait of the next defer: FFmpeg really has exited
	// readErr means the stream could not be read (most often because FFmpeg failed). Its message
	// waits for FFmpeg to end: stderr is only complete after Wait.
	var readErr error
	waited := false
	defer func() {
		if !waited {
			cancel()
			_ = cmd.Wait()
		}
		if readErr != nil {
			err = fmt.Errorf("%w: %s", readErr, stderr.String())
		}
	}()

	rd, err := NewReader(bufio.NewReaderSize(stdout, 1<<20))
	if err != nil {
		readErr = err
		return nil
	}
	if err := onInit(rd.Init()); err != nil {
		return stopIsNil(err)
	}
	for {
		f, err := rd.Next()
		if errors.Is(err, io.EOF) {
			// End of the stream: FFmpeg exits on its own and its exit code says whether it produced
			// everything.
			waited = true
			werr := cmd.Wait()
			if ctx.Err() != nil {
				return ctx.Err() // FFmpeg was stopped: the end of the stream is not the end of the file
			}
			if werr != nil {
				return fmt.Errorf("fmp4: FFmpeg: %w: %s", werr, stderr.String())
			}
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			readErr = err
			return nil
		}
		if err := onFragment(f); err != nil {
			return stopIsNil(err)
		}
	}
}

func stopIsNil(err error) error {
	if errors.Is(err, ErrStop) {
		return nil
	}
	return err
}

// tail keeps the end of FFmpeg's stderr, for error messages.
type tail struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}
