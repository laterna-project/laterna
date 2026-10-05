package transcode

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/proc"
)

// trialGrace is how long a trial that was killed gets to actually disappear.
const trialGrace = 5 * time.Second

// trial runs FFmpeg once to find out whether something works on this machine, for at most limit.
// The error carries what FFmpeg wrote to its error output.
//
// A process stuck in the kernel cannot be killed, and waiting for it would hang the detection for
// good: seen with h264_videotoolbox in a virtual machine. Past the limit and a short grace, the
// process is left behind and the trial counts as failed.
func trial(ctx context.Context, limit time.Duration, ffmpeg string, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := proc.Command(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	failed := func(err error) error {
		if err == nil {
			return nil
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	select {
	case err := <-done:
		return failed(err)
	case <-ctx.Done():
	}
	// The context has killed the process, which normally ends at once.
	select {
	case err := <-done:
		return failed(err)
	case <-time.After(trialGrace):
		// stderr may still be written to: it is not read here.
		return fmt.Errorf("%w: the process could not be stopped", ctx.Err())
	}
}
