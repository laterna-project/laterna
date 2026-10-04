package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/migrations"
)

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Hot backup, then restore: the copy holds what was written at the time and nothing after. Restored
// at startup, it replaces the database, which is kept aside.
func TestBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	backups := filepath.Join(data, "backups")
	st, err := Open(ctx, filepath.Join(data, FileName))
	if err != nil {
		t.Fatal(err)
	}
	before := newLibrary("Films", domain.LibraryMovies, "/m")
	mustWrite(t, st, func(q Q) error { return q.CreateLibrary(ctx, before) })
	copyPath := filepath.Join(backups, "laterna-manual-20261004T030000Z.db")
	if err := st.Backup(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	if got := files(t, backups); len(got) != 1 {
		t.Fatalf("backup files: %v", got)
	}
	if v, err := Check(ctx, copyPath); err != nil || v != LatestVersion() || v < 23 {
		t.Fatalf("backup check: %d %v", v, err)
	}
	after := newLibrary("Séries", domain.LibraryShows, "/s")
	mustWrite(t, st, func(q Q) error { return q.CreateLibrary(ctx, after) })

	// Nothing to restore: nothing happens.
	if restored, _, err := ApplyRestore(ctx, data, backups, time.Now()); restored || err != nil {
		t.Fatalf("restore without a request: %v %v", restored, err)
	}
	if err := StageRestore(ctx, data, copyPath); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	restored, kept, err := ApplyRestore(ctx, data, backups, time.Date(2026, 10, 4, 4, 0, 0, 0, time.UTC))
	if err != nil || !restored || filepath.Base(kept) != "laterna-before-restore-20261004T040000Z.db" {
		t.Fatalf("restore: %v %q %v", restored, kept, err)
	}
	st, err = Open(ctx, filepath.Join(data, FileName))
	if err != nil {
		t.Fatal(err)
	}
	libs, err := st.Read().Libraries(ctx)
	if err != nil || len(libs) != 1 || libs[0].Name != "Films" {
		t.Errorf("restored database: %v %v", libs, err)
	}
	_ = st.Close()
	// The replaced database is kept, with what was written since the backup.
	old, err := Open(ctx, kept)
	if err != nil {
		t.Fatal(err)
	}
	if libs, err := old.Read().Libraries(ctx); err != nil || len(libs) != 2 {
		t.Errorf("database kept aside: %v %v", libs, err)
	}
	_ = old.Close()
	if _, err := os.Stat(filepath.Join(data, RestoreName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("restore still pending: %v", err)
	}
}

func TestCheckRefuses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	junk := filepath.Join(dir, "not-a-database.db")
	if err := os.WriteFile(junk, []byte("bonjour"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(ctx, junk); err == nil {
		t.Error("random file accepted")
	}
	if _, err := Check(ctx, filepath.Join(dir, "absent.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	// A database from a newer version of Laterna.
	st, err := Open(ctx, filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.writer.ExecContext(ctx, "INSERT INTO goose_db_version (version_id, is_applied) VALUES (9999, 1)"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if _, err := Check(ctx, filepath.Join(dir, FileName)); !errors.Is(err, ErrNewerSchema) {
		t.Errorf("newer database: %v", err)
	}
	// A rejected restore is moved aside: the server starts on its own database.
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, RestoreName), []byte("bonjour"), 0o600); err != nil {
		t.Fatal(err)
	}
	if restored, _, err := ApplyRestore(ctx, data, filepath.Join(data, "backups"), time.Now()); restored || err == nil {
		t.Errorf("restore of a random file: %v %v", restored, err)
	}
	if got := files(t, data); len(got) != 1 || !strings.HasSuffix(got[0], ".rejected") {
		t.Errorf("rejected file not moved aside: %v", got)
	}
}

// Before migrations are applied to an existing database, it is backed up.
func TestBackupBeforeMigration(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	backups := filepath.Join(data, "backups")
	path := filepath.Join(data, FileName)
	st, err := Open(ctx, path, WithBackupDir(backups))
	if err != nil {
		t.Fatal(err)
	}
	if got := files(t, backups); len(got) != 0 {
		t.Fatalf("backup of a fresh database: %v", got)
	}
	provider, err := goose.NewProvider(database.DialectSQLite3, st.writer, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	st, err = Open(ctx, path, WithBackupDir(backups))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	got := files(t, backups)
	if len(got) != 1 || !strings.HasPrefix(got[0], "laterna-migration-v22-") {
		t.Fatalf("backup before migration: %v", got)
	}
	if v, err := Check(ctx, filepath.Join(backups, got[0])); err != nil || v != 22 {
		t.Errorf("backup version: %d %v", v, err)
	}
	// Up to date: no new backup.
	st, err = Open(ctx, path, WithBackupDir(backups))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if got := files(t, backups); len(got) != 1 {
		t.Errorf("backup without a migration: %v", got)
	}
}
