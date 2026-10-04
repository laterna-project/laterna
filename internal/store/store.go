// Package store manages the SQLite database: opening, migrations, typed queries (sqlc).
//
// SQLite only takes one writer at a time, so there is a single write connection (BEGIN IMMEDIATE
// transactions, hence never a conflict in the middle of one) and a pool of read-only connections
// that read in parallel thanks to WAL mode.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"modernc.org/sqlite" // pure Go SQLite driver (no cgo)
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/laterna-project/laterna/migrations"
)

// readerCache is the page cache of each read connection, in KiB (2,000 by default). Memory that
// SQLite frees stays with the process (modernc's allocator keeps it for reuse), so what has to be
// capped is what it allocates. Pages come back from the OS cache, with no measurable effect on
// lists.
const readerCache = 512

// FileName is the name of the database in the data folder.
const FileName = "laterna.db"

// Store gives access to the database.
type Store struct {
	writer *sql.DB
	reader *sql.DB
	read   Q
	path   string
}

// Open opens (or creates) the database at path and applies the pending migrations.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	var o openOptions
	for _, opt := range opts {
		opt(&o)
	}
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: invalid database path %q (characters ? or #)", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	common := []string{"busy_timeout(10000)", "foreign_keys(ON)", "synchronous(NORMAL)"}
	// analysis_limit caps the cost of ANALYZE (planner statistics, see Optimize).
	writer, err := sql.Open("sqlite", dsn(path, append([]string{"journal_mode(WAL)", "analysis_limit(1000)"}, common...), "immediate"))
	if err != nil {
		return nil, fmt.Errorf("store: opening for writing: %w", err)
	}
	writer.SetMaxOpenConns(1)
	// The write connection must never be recycled: it holds the write lock.
	writer.SetConnMaxLifetime(0)
	writer.SetMaxIdleConns(1)

	if err := migrate(ctx, writer, o.backupDir); err != nil {
		_ = writer.Close()
		return nil, err
	}
	// Statistics for the tables that lack them (0x10002: even those this connection has not read
	// yet), as SQLite recommends when opening.
	if _, err := writer.ExecContext(ctx, "PRAGMA optimize=0x10002"); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("store: statistics: %w", err)
	}

	reader, err := sql.Open("sqlite", dsn(path, append([]string{"query_only(ON)", fmt.Sprintf("cache_size(-%d)", readerCache)}, common...), ""))
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("store: opening for reading: %w", err)
	}
	readers := max(4, runtime.NumCPU())
	reader.SetMaxOpenConns(readers)
	reader.SetMaxIdleConns(readers)

	return &Store{writer: writer, reader: reader, read: newQ(reader), path: path}, nil
}

// dsn builds the connection string for the modernc driver: path, pragmas applied to each new
// connection, and the transaction locking mode.
func dsn(path string, pragmas []string, txlock string) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if txlock != "" {
		q.Set("_txlock", txlock)
	}
	return path + "?" + q.Encode()
}

// migrate applies the pending migrations. If backupDir is set and the database already existed, it
// is backed up there first.
func migrate(ctx context.Context, db *sql.DB, backupDir string) error {
	provider, err := goose.NewProvider(database.DialectSQLite3, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("store: migrations: %w", err)
	}
	if backupDir != "" {
		current, errVersion := provider.GetDBVersion(ctx)
		pending, errPending := provider.HasPending(ctx)
		if errVersion == nil && errPending == nil && pending && current > 0 {
			dst := filepath.Join(backupDir, fmt.Sprintf("laterna-migration-v%d-%s.db", current, time.Now().UTC().Format(BackupStamp)))
			if err := backupInto(ctx, db, dst); err != nil {
				return fmt.Errorf("store: backup before migration: %w", err)
			}
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("store: applying migrations: %w", err)
	}
	return nil
}

// Optimize refreshes the planner statistics of the tables that changed a lot. Without them SQLite
// may pick a less selective index: listing the episodes of a series by scanning every episode, for
// one. Cheap when nothing changed.
func (s *Store) Optimize(ctx context.Context) error {
	_, err := s.writer.ExecContext(ctx, "PRAGMA optimize")
	return err
}

// Read returns read-only access (the read pool: a write there would fail).
func (s *Store) Read() Q { return s.read }

// Size returns the space taken by the database and its WAL, in bytes.
func (s *Store) Size() int64 {
	var total int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		if st, err := os.Stat(p); err == nil {
			total += st.Size()
		}
	}
	return total
}

// Write runs fn in a write transaction, committed if fn succeeds and rolled back otherwise. Reads
// made in fn see the state of the transaction.
func (s *Store) Write(ctx context.Context, fn func(q Q) error) (err error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: beginning transaction: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if err = fn(newQ(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the connections.
func (s *Store) Close() error {
	return errors.Join(s.reader.Close(), s.writer.Close())
}

// ErrNotFound means a row is missing.
var ErrNotFound = sql.ErrNoRows

// ErrDuplicate means a uniqueness constraint was violated (name already taken...).
var ErrDuplicate = errors.New("store: value already present")

// IsNotFound reports a missing row.
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// translate converts SQLite driver errors into this package's errors.
func translate(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && (se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
		return fmt.Errorf("%w: %w", ErrDuplicate, err)
	}
	return err
}
