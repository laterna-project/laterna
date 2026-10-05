package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/config"
	"github.com/laterna-project/laterna/internal/platform"
	"github.com/laterna-project/laterna/internal/store"
)

// Backup then restore from the command line: "laterna backup" copies the database, "laterna
// restore" prepares the restore, and the server applies it at startup and keeps the database it
// replaces.
func TestBackupRestoreCommands(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dirs := platform.Dirs{Data: filepath.Join(root, "café"), Cache: filepath.Join(root, "cache"), Metadata: filepath.Join(root, "metadata")}
	t.Setenv("LATERNA_CONFIG", "")
	t.Setenv("LATERNA_DATA_DIR", dirs.Data)
	t.Setenv("LATERNA_CACHE_DIR", dirs.Cache)
	t.Setenv("LATERNA_METADATA_DIR", dirs.Metadata)
	t.Chdir(root) // no laterna.toml from the current folder
	var out, errOut bytes.Buffer
	exec := func(args ...string) int {
		t.Helper()
		out.Reset()
		errOut.Reset()
		return run(ctx, args, &out, &errOut)
	}
	if code := exec("backup"); code == 0 {
		t.Error("backup without a database accepted")
	}
	if code := exec("migrate"); code != 0 {
		t.Fatalf("migrate: %s", errOut.String())
	}
	setting := func(value string) {
		t.Helper()
		st, err := store.Open(ctx, filepath.Join(dirs.Data, store.FileName))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		if err := st.Write(ctx, func(q store.Q) error { return q.SetSetting(ctx, "test", value) }); err != nil {
			t.Fatal(err)
		}
	}
	setting("before the backup")
	if code := exec("backup"); code != 0 || !strings.Contains(out.String(), "database backed up") {
		t.Fatalf("backup: %d %s %s", code, out.String(), errOut.String())
	}
	backups, err := os.ReadDir(dirs.BackupDir())
	if err != nil || len(backups) != 1 || !strings.HasPrefix(backups[0].Name(), "laterna-manual-") {
		t.Fatalf("backups: %v %v", backups, err)
	}
	setting("after the backup")

	if code := exec("restore"); code == 0 {
		t.Error("restore without an argument accepted")
	}
	if code := exec("restore", filepath.Join(root, "absent.db")); code == 0 {
		t.Error("restore of a missing file accepted")
	}
	// By name only: looked up in the backup folder.
	if code := exec("restore", backups[0].Name()); code != 0 || !strings.Contains(out.String(), "restart the server") {
		t.Fatalf("restore: %d %s %s", code, out.String(), errOut.String())
	}

	// The server applies the restore at startup.
	cfg := config.Default()
	cfg.Server.Address = "127.0.0.1:0"
	serveCtx, cancel := context.WithCancel(ctx)
	ready, done := make(chan string, 1), make(chan error, 1)
	go func() {
		done <- serve(serveCtx, cfg, dirs, slog.New(slog.DiscardHandler), nil, func(addr string) { ready <- addr })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("the server stopped while starting: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the server did not start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	st, err := store.Open(ctx, filepath.Join(dirs.Data, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if v, _, err := st.Read().Setting(ctx, "test"); err != nil || v != "before the backup" {
		t.Errorf("database after the restore: %q %v", v, err)
	}
	backups, _ = os.ReadDir(dirs.BackupDir())
	var names []string
	for _, b := range backups {
		names = append(names, b.Name())
	}
	if len(names) != 2 || !strings.HasPrefix(names[0], "laterna-before-restore-") {
		t.Errorf("replaced database not kept: %v", names)
	}
}
