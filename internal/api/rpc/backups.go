package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
)

// Database backups.

var backupKinds = map[app.BackupKind]laternav1.BackupKind{
	app.BackupAuto:      laternav1.BackupKind_BACKUP_KIND_AUTO,
	app.BackupManual:    laternav1.BackupKind_BACKUP_KIND_MANUAL,
	app.BackupMigration: laternav1.BackupKind_BACKUP_KIND_MIGRATION,
	app.BackupReplaced:  laternav1.BackupKind_BACKUP_KIND_REPLACED,
}

func backupMsg(b app.Backup) *laternav1.Backup {
	return &laternav1.Backup{Name: b.Name, Kind: backupKinds[b.Kind], SizeBytes: b.Size, CreatedAt: timestamppb.New(b.CreatedAt)}
}

// ListBackups lists the backups.
func (s *SystemService) ListBackups(context.Context, *connect.Request[laternav1.ListBackupsRequest]) (*connect.Response[laternav1.ListBackupsResponse], error) {
	backups, err := s.app.Backups()
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListBackupsResponse{RestorePending: s.app.RestorePending()}
	for _, b := range backups {
		resp.Backups = append(resp.Backups, backupMsg(b))
	}
	return connect.NewResponse(resp), nil
}

// CreateBackup backs the database up right now.
func (s *SystemService) CreateBackup(ctx context.Context, _ *connect.Request[laternav1.CreateBackupRequest]) (*connect.Response[laternav1.CreateBackupResponse], error) {
	b, err := s.app.CreateBackup(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreateBackupResponse{Backup: backupMsg(b)}), nil
}

// DeleteBackup deletes a backup.
func (s *SystemService) DeleteBackup(ctx context.Context, req *connect.Request[laternav1.DeleteBackupRequest]) (*connect.Response[laternav1.DeleteBackupResponse], error) {
	if err := s.app.DeleteBackup(ctx, principal(ctx), req.Msg.GetName()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteBackupResponse{}), nil
}

// RestoreBackup prepares the restore of a backup.
func (s *SystemService) RestoreBackup(ctx context.Context, req *connect.Request[laternav1.RestoreBackupRequest]) (*connect.Response[laternav1.RestoreBackupResponse], error) {
	if err := s.app.RestoreBackup(ctx, principal(ctx), req.Msg.GetName()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.RestoreBackupResponse{}), nil
}

// CancelRestore cancels a prepared restore.
func (s *SystemService) CancelRestore(context.Context, *connect.Request[laternav1.CancelRestoreRequest]) (*connect.Response[laternav1.CancelRestoreResponse], error) {
	cancelled, err := s.app.CancelRestore()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CancelRestoreResponse{Cancelled: cancelled}), nil
}
