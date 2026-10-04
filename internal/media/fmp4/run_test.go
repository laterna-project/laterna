package fmp4

import (
	"context"
	"errors"
	"testing"

	"github.com/laterna-project/laterna/internal/media/remux"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

func TestRunStopAndCancel(t *testing.T) {
	path := testfixtures.Path(t, "Films/Deux Pistes (2019)/Deux Pistes (2019).mkv")
	ffmpeg, _, _ := testfixtures.FFmpeg()
	cmd := Command{Bin: ffmpeg, Args: remux.Args(remux.Options{Path: path, Audio: -1})}
	n := 0
	err := Run(context.Background(), cmd,
		func([]byte) error { return nil },
		func(Fragment) error {
			n++
			if n == 2 {
				return ErrStop
			}
			return nil
		})
	if err != nil || n != 2 {
		t.Errorf("stop requested: %v after %d fragments", err, n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	err = Run(ctx, cmd, func([]byte) error { cancel(); return nil }, func(Fragment) error { return nil })
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}

	boom := errors.New("disk full")
	if err := Run(context.Background(), cmd, func([]byte) error { return boom }, func(Fragment) error { return nil }); !errors.Is(err, boom) {
		t.Errorf("callback error: %v", err)
	}
	bad := Command{Bin: ffmpeg, Args: remux.Args(remux.Options{Path: path + ".absent", Audio: -1})}
	if err := Run(context.Background(), bad, func([]byte) error { return nil }, func(Fragment) error { return nil }); err == nil {
		t.Error("missing file accepted")
	}
}
