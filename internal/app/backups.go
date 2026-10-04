package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Backups. The database is copied, while in use, to the backup folder every night and before each
// migration (store.WithBackupDir). An administrator can list, create, download, delete and restore
// backups. A restore is prepared, then applied at the next start, because an open database cannot
// be replaced. Only the database is backed up: everything else (downloaded images, thumbnails,
// extracted subtitles) can be rebuilt from the media.

// BackupKind says where a backup comes from.
type BackupKind string

// Backup kinds.
const (
	// BackupAuto is a nightly backup, subject to rotation.
	BackupAuto BackupKind = "auto"
	// BackupManual was requested by an administrator and is kept until they delete it.
	BackupManual BackupKind = "manual"
	// BackupMigration was taken before a schema upgrade.
	BackupMigration BackupKind = "migration"
	// BackupReplaced is the database a restore replaced.
	BackupReplaced BackupKind = "before-restore"
)

// Backup is a database backup.
type Backup struct {
	Name      string
	Kind      BackupKind
	Size      int64
	CreatedAt time.Time
}

const (
	// keepOther is how many pre-migration and pre-restore backups are kept.
	keepOther = 3
	// autoBackupEvery is the minimum time between two automatic backups.
	autoBackupEvery = 20 * time.Hour
	maxBackupKeep   = 90
)

// backupName matches backups. Nothing else is listed, served or deleted.
var backupName = regexp.MustCompile(`^laterna-(auto|manual|migration-v\d+|before-restore)-\d{8}T\d{6}Z\.db$`)

func backupKind(name string) BackupKind {
	switch {
	case strings.HasPrefix(name, "laterna-auto-"):
		return BackupAuto
	case strings.HasPrefix(name, "laterna-migration-"):
		return BackupMigration
	case strings.HasPrefix(name, "laterna-before-restore-"):
		return BackupReplaced
	}
	return BackupManual
}

// backupsDir returns the backup folder. A server started without one (tests) makes no backups.
func (a *App) backupsDir() (string, error) {
	if a.backupDir == "" {
		return "", domain.Precondition("backup.no_directory")
	}
	return a.backupDir, nil
}

// Backups lists the backups, newest first.
func (a *App) Backups() ([]Backup, error) {
	dir, err := a.backupsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, e := range entries {
		if e.IsDir() || !backupName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{Name: e.Name(), Kind: backupKind(e.Name()), Size: info.Size(), CreatedAt: info.ModTime()})
	}
	slices.SortFunc(out, func(x, y Backup) int { return y.CreatedAt.Compare(x.CreatedAt) })
	return out, nil
}

// newBackup writes a backup of the given kind.
func (a *App) newBackup(ctx context.Context, kind BackupKind) (Backup, error) {
	dir, err := a.backupsDir()
	if err != nil {
		return Backup{}, err
	}
	name := "laterna-" + string(kind) + "-" + a.now().UTC().Format(store.BackupStamp) + ".db"
	path := filepath.Join(dir, name)
	if err := a.store.Backup(ctx, path); err != nil {
		return Backup{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Backup{}, err
	}
	return Backup{Name: name, Kind: kind, Size: info.Size(), CreatedAt: info.ModTime()}, nil
}

// CreateBackup backs the database up right now, at an administrator's request.
func (a *App) CreateBackup(ctx context.Context, p domain.Principal) (Backup, error) {
	b, err := a.newBackup(ctx, BackupManual)
	if err != nil {
		return Backup{}, err
	}
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.backup_created", "name", b.Name)})
	return b, nil
}

// autoBackup is the nightly job: a backup if the last automatic one is more than 20 h old, then the
// rotation.
func (a *App) autoBackup(ctx context.Context, _ string) error {
	keep := a.Settings().BackupKeep
	if keep <= 0 || a.backupDir == "" {
		return nil
	}
	backups, err := a.Backups()
	if err != nil {
		return err
	}
	recent := slices.ContainsFunc(backups, func(b Backup) bool {
		return b.Kind == BackupAuto && a.now().Sub(b.CreatedAt) < autoBackupEvery
	})
	if !recent {
		b, err := a.newBackup(ctx, BackupAuto)
		if err != nil {
			return err
		}
		a.log.InfoContext(ctx, "database backed up", "name", b.Name, "size", b.Size)
	}
	return a.pruneBackups(keep)
}

// pruneBackups applies the rotation: keep automatic backups, three pre-migration ones and three
// pre-restore ones. Manual ones stay.
func (a *App) pruneBackups(keep int) error {
	backups, err := a.Backups() // newest first
	if err != nil {
		return err
	}
	limits := map[BackupKind]int{BackupAuto: keep, BackupMigration: keepOther, BackupReplaced: keepOther}
	seen := map[BackupKind]int{}
	for _, b := range backups {
		limit, rotated := limits[b.Kind]
		if !rotated {
			continue
		}
		if seen[b.Kind]++; seen[b.Kind] > limit {
			if err := os.Remove(filepath.Join(a.backupDir, b.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// backupPath returns the path of an existing backup, never leaving the folder.
func (a *App) backupPath(name string) (string, error) {
	dir, err := a.backupsDir()
	if err != nil {
		return "", err
	}
	if !backupName.MatchString(name) {
		return "", domain.NotFound("backup.not_found")
	}
	path := filepath.Join(dir, name)
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", domain.NotFound("backup.not_found")
	}
	return path, nil
}

// OpenBackup opens a backup for download (to store it somewhere safe).
func (a *App) OpenBackup(name string) (*os.File, error) {
	path, err := a.backupPath(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, domain.NotFound("backup.not_found")
	}
	return f, nil
}

// DeleteBackup deletes a backup.
func (a *App) DeleteBackup(ctx context.Context, p domain.Principal, name string) error {
	path, err := a.backupPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	a.record(ctx, domain.Activity{Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID, Text: domain.T("activity.backup_deleted", "name", name)})
	return nil
}

// RestoreBackup prepares the restore of a backup. Once checked (readable, from a version this
// server can open) it will replace the database at the next start, and the current database will be
// kept aside.
func (a *App) RestoreBackup(ctx context.Context, p domain.Principal, name string) error {
	path, err := a.backupPath(name)
	if err != nil {
		return err
	}
	if a.dataDir == "" {
		return domain.Precondition("backup.no_directory")
	}
	if err := store.StageRestore(ctx, a.dataDir, path); err != nil {
		if errors.Is(err, store.ErrNewerSchema) {
			return domain.Invalid("backup.too_recent")
		}
		return domain.Invalid("backup.unusable", "reason", err)
	}
	a.log.WarnContext(ctx, "restore staged: it will be applied at the next start", "name", name, "by", p.Account.Username)
	a.record(ctx, domain.Activity{
		Kind: domain.ActivitySettingsUpdated, AccountID: &p.Account.ID,
		Text: domain.T("activity.restore_staged", "name", name),
	})
	return nil
}

// CancelRestore cancels a prepared restore; false if there was none.
func (a *App) CancelRestore() (bool, error) {
	if a.dataDir == "" {
		return false, nil
	}
	err := os.Remove(filepath.Join(a.dataDir, store.RestoreName))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// RestorePending reports whether a restore is waiting for the next start.
func (a *App) RestorePending() bool {
	if a.dataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(a.dataDir, store.RestoreName))
	return err == nil
}
