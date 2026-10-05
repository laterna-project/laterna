package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/store"
)

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestServerIDIsStableAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), store.FileName)

	st := openStore(t, path)
	first, err := New(ctx, st, Options{ServerName: "Living room"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Server().ID.IsZero() || first.Server().Name != "Living room" {
		t.Fatalf("unexpected identity: %+v", first.Server())
	}
	_ = st.Close()

	st = openStore(t, path)
	defer func() { _ = st.Close() }()
	second, err := New(ctx, st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Server().ID != first.Server().ID {
		t.Errorf("the ID changed across a restart: %s -> %s", first.Server().ID, second.Server().ID)
	}
	if second.Server().Name == "" {
		t.Error("without a configured name, want the host name")
	}
}

// A forced encoder that is unknown is a typo: refused at startup, with the list of known ones.
func TestUnknownEncoderRefused(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), store.FileName))
	defer func() { _ = st.Close() }()
	if _, err := New(context.Background(), st, Options{Encoder: "h264_nvnec"}); err == nil || !strings.Contains(err.Error(), "libx264") {
		t.Errorf("unknown encoder: %v", err)
	}
}

// Stopping background work does not wait for the encoder detection started at startup (about ten
// seconds on an idle machine, much more on a busy one): an FFmpeg that does not return is
// interrupted.
func TestStopDoesNotWaitForEncoderDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake FFmpeg is a shell script")
	}
	stuck := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(stuck, []byte("#!/bin/sh\nexec sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, filepath.Join(t.TempDir(), store.FileName))
	defer func() { _ = st.Close() }()
	a, err := New(context.Background(), st, Options{FFmpeg: stuck, NoAutoScans: true, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	stopped := make(chan struct{})
	go func() {
		a.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stopping waits for the encoder detection to finish")
	}
	if _, err := a.encoders(); err == nil {
		t.Error("detection interrupted: want no encoder")
	}
}
