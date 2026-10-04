package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// KnownFile is the known state of a file, as the scan compares it to the disk.
type KnownFile struct {
	ID           domain.ID
	Path         string
	Size         int64
	ModTime      time.Time
	Fingerprint  string
	MissingSince *time.Time
	Analyzed     bool
	// SubtitlesFingerprint and SubtitlesSidecars are the file's fingerprint and the signature of
	// its external subtitles at the last extraction (empty if never extracted).
	SubtitlesFingerprint, SubtitlesSidecars string
	// TrickplayFingerprint is the file's fingerprint when its scrubbing thumbnails were generated
	// (empty if never).
	TrickplayFingerprint string
	// SegmentsFingerprint is the file's fingerprint when its intro and credits were looked for
	// (empty if never). SegmentsAudio means the search used audio.
	SegmentsFingerprint string
	SegmentsAudio       bool
}

// LibraryFiles lists the known files of a library.
func (q Q) LibraryFiles(ctx context.Context, libraryID domain.ID) ([]KnownFile, error) {
	rows, err := q.q.ListLibraryFiles(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	out := make([]KnownFile, len(rows))
	for i, r := range rows {
		out[i] = KnownFile{
			ID: r.ID, Path: r.Path, Size: r.Size, ModTime: fromMillis(r.Mtime), Fingerprint: r.Fingerprint,
			MissingSince: optTime(r.MissingSince), Analyzed: r.AnalyzedAt.Valid,
			SubtitlesFingerprint: r.SubtitlesFingerprint, SubtitlesSidecars: r.SubtitlesSidecars,
			TrickplayFingerprint: r.TrickplayFingerprint,
			SegmentsFingerprint:  r.SegmentsFingerprint, SegmentsAudio: r.SegmentsAudio == 1,
		}
	}
	return out, nil
}

// FileItem is the item of a present file: for an episode, with its season and series; for a track,
// with its album and artist.
type FileItem struct {
	Path     string
	ItemID   domain.ID
	Kind     domain.ItemKind
	SeasonID *domain.ID
	SeriesID *domain.ID
	AlbumID  *domain.ID
	ArtistID *domain.ID
}

// PresentFileItems lists the item of each present file of a library.
func (q Q) PresentFileItems(ctx context.Context, libraryID domain.ID) ([]FileItem, error) {
	rows, err := q.q.ListPresentFileItems(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	out := make([]FileItem, len(rows))
	for i, r := range rows {
		out[i] = FileItem{
			Path: r.Path, ItemID: r.ItemID, Kind: domain.ItemKind(r.Kind), SeasonID: r.SeasonID, SeriesID: r.SeriesID,
			AlbumID: r.AlbumID, ArtistID: r.ArtistID,
		}
	}
	return out, nil
}

// CreateFile stores a new file (not analyzed yet).
func (q Q) CreateFile(ctx context.Context, f domain.MediaFile, now time.Time) error {
	return translate(q.q.InsertFile(ctx, sqlc.InsertFileParams{
		ID: f.ID, LibraryID: f.LibraryID, Path: f.Path, Size: f.Size, Mtime: toMillis(f.ModTime),
		Fingerprint: f.Fingerprint, CreatedAt: toMillis(now), UpdatedAt: toMillis(now),
	}))
}

// MoveFile stores the new path of a file that was moved or renamed (same content).
func (q Q) MoveFile(ctx context.Context, id domain.ID, path string, now time.Time) error {
	return translate(q.q.MoveFile(ctx, sqlc.MoveFileParams{Path: path, UpdatedAt: toMillis(now), ID: id}))
}

// UpdateFileContent stores a changed file. It will be analyzed again.
func (q Q) UpdateFileContent(ctx context.Context, id domain.ID, size int64, mtime time.Time, fingerprint string, now time.Time) error {
	return q.q.UpdateFileContent(ctx, sqlc.UpdateFileContentParams{
		Size: size, Mtime: toMillis(mtime), Fingerprint: fingerprint, UpdatedAt: toMillis(now), ID: id,
	})
}

// MarkFileMissing records that a file is missing (the first date it went missing is kept).
func (q Q) MarkFileMissing(ctx context.Context, id domain.ID, now time.Time) error {
	return q.q.MarkFileMissing(ctx, sqlc.MarkFileMissingParams{
		MissingSince: nullMillis(&now), UpdatedAt: toMillis(now), ID: id,
	})
}

// MarkFilePresent clears the missing state of a file that came back.
func (q Q) MarkFilePresent(ctx context.Context, id domain.ID, now time.Time) error {
	return q.q.MarkFilePresent(ctx, sqlc.MarkFilePresentParams{UpdatedAt: toMillis(now), ID: id})
}

// FilesMissingBefore lists the files that have been missing since before the given date.
func (q Q) FilesMissingBefore(ctx context.Context, libraryID domain.ID, before time.Time) ([]domain.ID, error) {
	return q.q.ListFilesMissingBefore(ctx, sqlc.ListFilesMissingBeforeParams{
		LibraryID: libraryID, MissingSince: nullMillis(&before),
	})
}

// DeleteFile deletes a file (and its link to an item).
func (q Q) DeleteFile(ctx context.Context, id domain.ID) error { return q.q.DeleteFile(ctx, id) }

// File reads a file and its analysis.
func (q Q) File(ctx context.Context, id domain.ID) (domain.MediaFile, error) {
	row, err := q.q.GetFile(ctx, id)
	if err != nil {
		return domain.MediaFile{}, err
	}
	return q.fileWithInfo(ctx, fileRow{
		id: row.ID, libraryID: row.LibraryID, path: row.Path, size: row.Size, mtime: row.Mtime,
		fingerprint: row.Fingerprint, missing: row.MissingSince, analyzed: row.AnalyzedAt,
		container: row.Container, durationMs: row.DurationMs, bitrate: row.Bitrate, tags: row.Tags,
	})
}

// SetFileAnalysis stores the result of analyzing a file (streams and chapters included).
func (q Q) SetFileAnalysis(ctx context.Context, id domain.ID, info domain.MediaInfo, now time.Time) error {
	if err := q.q.SetFileAnalysis(ctx, sqlc.SetFileAnalysisParams{
		Container: info.Container, DurationMs: info.Duration.Milliseconds(), Bitrate: info.Bitrate,
		Tags: encodeTags(info.Tags), AnalyzedAt: nullMillis(&now), UpdatedAt: toMillis(now), ID: id,
	}); err != nil {
		return err
	}
	if err := q.q.DeleteFileStreams(ctx, id); err != nil {
		return err
	}
	for _, s := range info.Streams {
		if err := q.q.InsertStream(ctx, sqlc.InsertStreamParams{
			FileID: id, Idx: int64(s.Index), Kind: string(s.Kind), Codec: s.Codec, Profile: s.Profile,
			Language: s.Language, Title: s.Title, IsDefault: toInt(s.Default), IsForced: toInt(s.Forced),
			IsHearingImpaired: toInt(s.HearingImpaired), Width: int64(s.Width), Height: int64(s.Height),
			BitDepth: int64(s.BitDepth), FrameRate: s.FrameRate, DynamicRange: string(s.DynamicRange),
			PixelFormat: s.PixelFormat, Channels: int64(s.Channels), ChannelLayout: s.ChannelLayout,
			SampleRate: int64(s.SampleRate), Bitrate: s.Bitrate,
		}); err != nil {
			return err
		}
	}
	if err := q.q.DeleteFileChapters(ctx, id); err != nil {
		return err
	}
	// New content: its intro and credits must be looked for again.
	if err := q.q.DeleteFileSegments(ctx, id); err != nil {
		return err
	}
	for i, c := range info.Chapters {
		if err := q.q.InsertChapter(ctx, sqlc.InsertChapterParams{
			FileID: id, Idx: int64(i), StartMs: c.Start.Milliseconds(), EndMs: c.End.Milliseconds(), Title: c.Title,
		}); err != nil {
			return err
		}
	}
	return nil
}

// SetFileAnalysisError records that analyzing a file failed.
func (q Q) SetFileAnalysisError(ctx context.Context, id domain.ID, msg string, now time.Time) error {
	return q.q.SetFileAnalysisError(ctx, sqlc.SetFileAnalysisErrorParams{AnalysisError: msg, UpdatedAt: toMillis(now), ID: id})
}

type fileRow struct {
	id, libraryID       domain.ID
	path, fingerprint   string
	container, tags     string
	size, mtime         int64
	durationMs, bitrate int64
	missing, analyzed   sql.NullInt64
}

func (q Q) fileWithInfo(ctx context.Context, r fileRow) (domain.MediaFile, error) {
	f := domain.MediaFile{
		ID: r.id, LibraryID: r.libraryID, Path: r.path, Size: r.size, ModTime: fromMillis(r.mtime),
		Fingerprint: r.fingerprint,
		Info: domain.MediaInfo{
			Container: r.container, Duration: time.Duration(r.durationMs) * time.Millisecond, Bitrate: r.bitrate,
			Tags: decodeTags(r.tags),
		},
	}
	f.MissingSince = optTime(r.missing)
	f.AnalyzedAt = optTime(r.analyzed)
	streams, err := q.q.ListFileStreams(ctx, r.id)
	if err != nil {
		return domain.MediaFile{}, err
	}
	for _, s := range streams {
		f.Info.Streams = append(f.Info.Streams, domain.Stream{
			Index: int(s.Idx), Kind: domain.StreamKind(s.Kind), Codec: s.Codec, Profile: s.Profile,
			Language: s.Language, Title: s.Title, Default: s.IsDefault == 1, Forced: s.IsForced == 1,
			HearingImpaired: s.IsHearingImpaired == 1, Width: int(s.Width), Height: int(s.Height),
			BitDepth: int(s.BitDepth), FrameRate: s.FrameRate, DynamicRange: domain.DynamicRange(s.DynamicRange),
			PixelFormat: s.PixelFormat, Channels: int(s.Channels), ChannelLayout: s.ChannelLayout,
			SampleRate: int(s.SampleRate), Bitrate: s.Bitrate,
		})
	}
	chapters, err := q.q.ListFileChapters(ctx, r.id)
	if err != nil {
		return domain.MediaFile{}, err
	}
	for _, c := range chapters {
		f.Info.Chapters = append(f.Info.Chapters, domain.Chapter{
			Start: time.Duration(c.StartMs) * time.Millisecond, End: time.Duration(c.EndMs) * time.Millisecond, Title: c.Title,
		})
	}
	if f.Segments, err = q.fileSegments(ctx, r.id); err != nil {
		return domain.MediaFile{}, err
	}
	return f, nil
}

// Scrubbing thumbnails.

// SetTrickplay stores the scrubbing thumbnails of a file (replacing the previous ones).
func (q Q) SetTrickplay(ctx context.Context, t domain.Trickplay) error {
	return q.q.UpsertTrickplay(ctx, sqlc.UpsertTrickplayParams{
		FileID: t.FileID, Fingerprint: t.Fingerprint, Key: t.Key, IntervalMs: t.Interval.Milliseconds(),
		Width: int64(t.Width), Height: int64(t.Height), Columns: int64(t.Columns), Rows: int64(t.Rows),
		Count: int64(t.Count), Sheets: int64(t.Sheets), CreatedAt: toMillis(t.CreatedAt),
	})
}

// Trickplay reads the scrubbing thumbnails of a file.
func (q Q) Trickplay(ctx context.Context, fileID domain.ID) (domain.Trickplay, error) {
	r, err := q.q.GetTrickplay(ctx, fileID)
	if err != nil {
		return domain.Trickplay{}, err
	}
	return domain.Trickplay{
		FileID: r.FileID, Fingerprint: r.Fingerprint, Key: r.Key, Interval: time.Duration(r.IntervalMs) * time.Millisecond,
		Width: int(r.Width), Height: int(r.Height), Columns: int(r.Columns), Rows: int(r.Rows),
		Count: int(r.Count), Sheets: int(r.Sheets), CreatedAt: fromMillis(r.CreatedAt),
	}, nil
}

// TrickplayKeys returns, for each file that has sheets, the key of their generation.
func (q Q) TrickplayKeys(ctx context.Context) (map[domain.ID]string, error) {
	rows, err := q.q.ListTrickplayKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.ID]string, len(rows))
	for _, r := range rows {
		out[r.FileID] = r.Key
	}
	return out, nil
}

// encodeTags writes the tags of a file as JSON (with UTF-8 cleaned up: a badly encoded tag must not
// block the analysis).
func encodeTags(tags map[string]string) string {
	clean := make(map[string]string, len(tags))
	for k, v := range tags {
		clean[strings.ToValidUTF8(k, "�")] = strings.ToValidUTF8(v, "�")
	}
	data, err := json.Marshal(clean)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// decodeTags reads back the tags of a file; nil if unreadable.
func decodeTags(s string) map[string]string {
	var tags map[string]string
	if json.Unmarshal([]byte(s), &tags) != nil || len(tags) == 0 {
		return nil
	}
	return tags
}
