package transcode

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

func TestTrial(t *testing.T) {
	testfixtures.Library(t) // skips the test without FFmpeg
	ffmpeg, _, _ := testfixtures.FFmpeg()
	ctx := context.Background()

	if err := trial(ctx, 10*time.Second, ffmpeg, []string{"-hide_banner", "-version"}); err != nil {
		t.Errorf("a command that works: %v", err)
	}

	// A failure carries what FFmpeg said.
	err := trial(ctx, 10*time.Second, ffmpeg, []string{"-hide_banner", "-v", "error", "-f", "lavfi", "-i", "nosuchsource", "-f", "null", "-"})
	if err == nil || !strings.Contains(err.Error(), "nosuchsource") {
		t.Errorf("a command that fails: %v", err)
	}

	// Past its limit a trial is killed, and that ends it well before the grace period.
	start := time.Now()
	err = trial(ctx, 300*time.Millisecond, ffmpeg, []string{"-hide_banner", "-v", "error", "-re", "-f", "lavfi", "-i", "testsrc2=duration=60", "-f", "null", "-"})
	if took := time.Since(start); err == nil || took > trialGrace {
		t.Errorf("a command that runs too long: %v after %v", err, took)
	}
}
