package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Offline downloads. plan is the preparation decision, as JSON; only the app layer reads it.

// CreateDownload stores a download.
func (q Q) CreateDownload(ctx context.Context, d domain.Download, plan string) error {
	return translate(q.q.InsertDownload(ctx, sqlc.InsertDownloadParams{
		ID: d.ID, AccountID: d.AccountID, ProfileID: d.ProfileID, SessionID: d.SessionID, ItemID: d.ItemID, FileID: d.FileID,
		Quality: string(d.Quality), State: string(d.State), Plan: plan, Progress: d.Progress, Estimate: d.Estimate,
		Size: d.Size, Path: d.Path, Error: d.Error, CreatedAt: toMillis(d.CreatedAt), UpdatedAt: toMillis(d.UpdatedAt),
		ReadyAt: nullMillis(d.ReadyAt),
	}))
}

// Download reads a download and its preparation decision.
func (q Q) Download(ctx context.Context, id domain.ID) (domain.Download, string, error) {
	r, err := q.q.GetDownload(ctx, id)
	if err != nil {
		return domain.Download{}, "", err
	}
	return downloadFromRow(r), r.Plan, nil
}

// SessionDownloads lists the downloads of a device for a profile, oldest first.
func (q Q) SessionDownloads(ctx context.Context, sessionID, profileID domain.ID) ([]domain.Download, error) {
	rows, err := q.q.ListSessionDownloads(ctx, sqlc.ListSessionDownloadsParams{SessionID: sessionID, ProfileID: profileID})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Download, len(rows))
	for i, r := range rows {
		out[i] = downloadFromRow(r)
	}
	return out, nil
}

// CountSessionDownloads returns the number of downloads of a device (all profiles).
func (q Q) CountSessionDownloads(ctx context.Context, sessionID domain.ID) (int64, error) {
	return q.q.CountSessionDownloads(ctx, sessionID)
}

// SetDownloadState stores the state of a download: progress, size, file, error.
func (q Q) SetDownloadState(ctx context.Context, d domain.Download) error {
	return q.q.UpdateDownloadState(ctx, sqlc.UpdateDownloadStateParams{
		State: string(d.State), Progress: d.Progress, Size: d.Size, Path: d.Path, Error: d.Error,
		ReadyAt: nullMillis(d.ReadyAt), UpdatedAt: toMillis(d.UpdatedAt), ID: d.ID,
	})
}

// SetDownloadProgress records the progress of a preparation under way.
func (q Q) SetDownloadProgress(ctx context.Context, id domain.ID, progress float64, now time.Time) error {
	return q.q.UpdateDownloadProgress(ctx, sqlc.UpdateDownloadProgressParams{Progress: progress, UpdatedAt: toMillis(now), ID: id})
}

// DeleteDownload forgets a download.
func (q Q) DeleteDownload(ctx context.Context, id domain.ID) error {
	return q.q.DeleteDownload(ctx, id)
}

// DeleteOldDownloads forgets the downloads that became ready, or failed, before cutoff, and returns
// how many there were.
func (q Q) DeleteOldDownloads(ctx context.Context, cutoff time.Time) (int64, error) {
	return q.q.DeleteOldDownloads(ctx, sql.NullInt64{Int64: toMillis(cutoff), Valid: true})
}

// DeleteOfflinePlaysBefore forgets the offline plays applied before t. A report replayed that long
// afterwards is no longer recognized.
func (q Q) DeleteOfflinePlaysBefore(ctx context.Context, t time.Time) error {
	return q.q.DeleteOfflinePlaysBefore(ctx, toMillis(t))
}

// DownloadIDs lists every known download.
func (q Q) DownloadIDs(ctx context.Context) ([]domain.ID, error) { return q.q.ListDownloadIDs(ctx) }

// SaveOfflineProgress records a playback that happened offline, at time at. A finished playback
// always counts (without wiping the resume point of a more recent one). A resume point is only kept
// if it is more recent than what the profile has done since. A playback that was already applied
// (same item, same time: a replayed report) is ignored, and applied is then false.
func (q Q) SaveOfflineProgress(ctx context.Context, profileID, itemID domain.ID, resume time.Duration, finished bool, at, now time.Time) (applied bool, err error) {
	n, err := q.q.InsertOfflinePlay(ctx, sqlc.InsertOfflinePlayParams{ProfileID: profileID, ItemID: itemID, PlayedAt: toMillis(at)})
	if err != nil || n == 0 {
		return false, err
	}
	last := sql.NullInt64{Int64: toMillis(at), Valid: true}
	if finished {
		return true, q.q.SaveOfflineFinished(ctx, sqlc.SaveOfflineFinishedParams{ProfileID: profileID, ItemID: itemID, LastPlayedAt: last, UpdatedAt: toMillis(now)})
	}
	return true, q.q.SaveOfflinePosition(ctx, sqlc.SaveOfflinePositionParams{
		ProfileID: profileID, ItemID: itemID, PositionMs: resume.Milliseconds(), LastPlayedAt: last, UpdatedAt: toMillis(now),
	})
}

func downloadFromRow(r sqlc.Download) domain.Download {
	return domain.Download{
		ID: r.ID, AccountID: r.AccountID, ProfileID: r.ProfileID, SessionID: r.SessionID, ItemID: r.ItemID, FileID: r.FileID,
		Quality: domain.DownloadQuality(r.Quality), State: domain.DownloadState(r.State), Progress: r.Progress,
		Estimate: r.Estimate, Size: r.Size, Path: r.Path, Error: r.Error, CreatedAt: fromMillis(r.CreatedAt),
		UpdatedAt: fromMillis(r.UpdatedAt), ReadyAt: optTime(r.ReadyAt),
	}
}
