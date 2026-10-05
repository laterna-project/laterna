package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// expectChange waits for a root to be reported (two seconds at most) and drains extra signals.
func expectChange(t *testing.T, w *Watcher, root string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case r := <-w.Changes():
			if r == root {
				drain(w)
				return
			}
		case <-timeout:
			t.Fatalf("no change reported under %s", root)
		}
	}
}

func drain(w *Watcher) {
	for {
		select {
		case <-w.Changes():
		case <-time.After(200 * time.Millisecond):
			return
		}
	}
}

func expectNothing(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case r := <-w.Changes():
		t.Fatalf("unexpected change under %s", r)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWatcher(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Movies")
	sub := filepath.Join(root, "Movie (2020)")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(func(err error) { t.Logf("error: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	missing := filepath.Join(t.TempDir(), "absent")
	failed, err := w.Sync([]string{root, missing})
	if len(failed) != 1 || failed[0] != missing || err == nil || w.Dirs() != 2 {
		t.Fatalf("watching: %v %v (%d folders)", failed, err, w.Dirs())
	}

	// File in an existing subfolder.
	if err := os.WriteFile(filepath.Join(sub, "movie.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectChange(t, w, root)
	// New folder, then a file in it: the folder gets watched too.
	fresh := filepath.Join(root, "New (2021)")
	if err := os.Mkdir(fresh, 0o750); err != nil {
		t.Fatal(err)
	}
	expectChange(t, w, root)
	if err := os.WriteFile(filepath.Join(fresh, "new.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectChange(t, w, root)
	// Hidden file: nothing.
	if err := os.WriteFile(filepath.Join(sub, ".DS_Store"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectNothing(t, w)
	// Removal.
	if err := os.Remove(filepath.Join(sub, "movie.mkv")); err != nil {
		t.Fatal(err)
	}
	expectChange(t, w, root)

	// Root no longer watched: nothing anymore.
	if _, err := w.Sync(nil); err != nil || w.Dirs() != 0 {
		t.Fatalf("forgetting: %v (%d folders)", err, w.Dirs())
	}
	if err := os.WriteFile(filepath.Join(sub, "other.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectNothing(t, w)
}
