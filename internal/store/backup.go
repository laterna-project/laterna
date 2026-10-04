package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/migrations"
)

// Database backups.

// RestoreName is the name, in the data folder, of the database to put in place at the next start
// (prepared by StageRestore, applied by ApplyRestore).
const RestoreName = FileName + ".restore"

// BackupStamp is the timestamp layout in backup names (UTC).
const BackupStamp = "20060102T150405Z"

// Option configures how the database is opened.
type Option func(*openOptions)

type openOptions struct {
	backupDir string
}

// WithBackupDir backs the database up into dir before applying migrations. If an upgrade goes
// wrong, putting that copy back ("laterna-migration-v<version>-...") undoes it.
func WithBackupDir(dir string) Option { return func(o *openOptions) { o.backupDir = dir } }

// Backup writes to dst a consistent copy of the database, taken while it is in use (VACUUM INTO:
// reads go on, writes wait for the copy to finish). It is checked before being put in place, so dst
// never exists half written.
func (s *Store) Backup(ctx context.Context, dst string) error { return backupInto(ctx, s.writer, dst) }

// BackupFile backs up the database in file src to dst without opening it the way Open does (no
// migration is applied). It is used by the command line, whether the server is running or not.
func BackupFile(ctx context.Context, src, dst string) error {
	db, err := sql.Open("sqlite", dsn(src, []string{"busy_timeout(10000)"}, ""))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return backupInto(ctx, db, dst)
}

func backupInto(ctx context.Context, db *sql.DB, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("copying the database: %w", err)
	}
	if _, err := Check(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("unreadable database copy: %w", err)
	}
	return os.Rename(tmp, dst)
}

// LatestVersion returns the schema version this binary can open: the number of its last migration.
func LatestVersion() int64 {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return 0
	}
	var latest int64
	for _, e := range entries {
		prefix, _, _ := strings.Cut(e.Name(), "_")
		if v, err := strconv.ParseInt(prefix, 10, 64); err == nil {
			latest = max(latest, v)
		}
	}
	return latest
}

// ErrNewerSchema is returned for a database written by a newer version of Laterna.
var ErrNewerSchema = errors.New("database from a newer version of Laterna")

// Check makes sure a file is a readable Laterna database that this binary can open, and returns its
// schema version.
func Check(ctx context.Context, path string) (int64, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", dsn(path, []string{"query_only(ON)"}, ""))
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return 0, fmt.Errorf("unreadable file: %w", err)
	}
	if check != "ok" {
		return 0, fmt.Errorf("damaged database: %s", check)
	}
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied = 1").Scan(&version); err != nil || !version.Valid {
		return 0, errors.New("not a Laterna database")
	}
	if version.Int64 > LatestVersion() {
		return version.Int64, fmt.Errorf("%w (schema %d, this server knows %d)", ErrNewerSchema, version.Int64, LatestVersion())
	}
	return version.Int64, nil
}

// StageRestore prepares the restore of a backup: it is checked, then copied next to the database,
// which it will replace at the next start (an open database cannot be replaced).
func StageRestore(ctx context.Context, dataDir, src string) error {
	if _, err := Check(ctx, src); err != nil {
		return err
	}
	dst := filepath.Join(dataDir, RestoreName)
	tmp := dst + ".tmp"
	if err := copyFile(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// ApplyRestore puts in place, before the database is opened, the one StageRestore prepared. The
// replaced database is kept in backupDir ("laterna-before-restore-...") and kept is its path.
// restored is false if there was nothing to restore.
func ApplyRestore(ctx context.Context, dataDir, backupDir string, now time.Time) (restored bool, kept string, err error) {
	staged := filepath.Join(dataDir, RestoreName)
	if _, err := os.Stat(staged); errors.Is(err, fs.ErrNotExist) {
		return false, "", nil
	} else if err != nil {
		return false, "", err
	}
	if _, err := Check(ctx, staged); err != nil {
		// It will never be applied: move it aside so the server starts on its current database.
		_ = os.Rename(staged, staged+".rejected")
		return false, "", fmt.Errorf("restore rejected: %w", err)
	}
	current := filepath.Join(dataDir, FileName)
	if _, err := os.Stat(current); err == nil {
		if err := os.MkdirAll(backupDir, 0o750); err != nil {
			return false, "", err
		}
		kept = filepath.Join(backupDir, "laterna-before-restore-"+now.UTC().Format(BackupStamp)+".db")
		// Take a consistent copy (WAL included), then remove the database and its WAL.
		if err := BackupFile(ctx, current, kept); err != nil {
			// The current database cannot be read (often the very reason for restoring): keep it as
			// it is.
			kept = filepath.Join(backupDir, "laterna-damaged-"+now.UTC().Format(BackupStamp)+".db")
			if err := os.Rename(current, kept); err != nil {
				return false, "", err
			}
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(current + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return false, "", err
			}
		}
	}
	if err := os.Rename(staged, current); err != nil {
		return false, "", err
	}
	return true, kept, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
