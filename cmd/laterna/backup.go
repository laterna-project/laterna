package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/laterna-project/laterna/internal/store"
)

// backupCommand backs the database up right now, whether the server is running or not: SQLite
// accepts a second process and the copy is consistent.
func backupCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_, dirs, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	db := filepath.Join(dirs.Data, store.FileName)
	if _, err := os.Stat(db); err != nil {
		return fmt.Errorf("no database to back up: %w", err)
	}
	dst := filepath.Join(dirs.BackupDir(), "laterna-manual-"+time.Now().UTC().Format(store.BackupStamp)+".db")
	if err := store.BackupFile(ctx, db, dst); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "database backed up:", dst)
	return nil
}

// restoreCommand prepares the restore of a backup: a path, or the name of a file in the backup
// folder. It is applied the next time the server starts. This is the way to restore when the server
// no longer starts.
func restoreCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_, dirs, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: laterna restore [-config <file>] <backup>")
	}
	src := fs.Arg(0)
	if _, err := os.Stat(src); err != nil {
		src = filepath.Join(dirs.BackupDir(), filepath.Base(fs.Arg(0)))
	}
	if err := store.StageRestore(ctx, dirs.Data, src); err != nil {
		return fmt.Errorf("unusable backup: %w", err)
	}
	_, _ = fmt.Fprintln(stdout, "restore staged from", src)
	_, _ = fmt.Fprintln(stdout, "restart the server: the current database will be kept in", dirs.BackupDir())
	return nil
}
