package store

import (
	"context"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Intros, credits and other skippable segments.

// SetChapterSegments replaces the segments of a file that come from its chapters. They win over
// those found by audio.
func (q Q) SetChapterSegments(ctx context.Context, fileID domain.ID, segs []domain.Segment) error {
	if err := q.q.DeleteFileSegmentsFrom(ctx, sqlc.DeleteFileSegmentsFromParams{FileID: fileID, Source: string(domain.SegmentFromChapters)}); err != nil {
		return err
	}
	for _, s := range segs {
		if err := q.q.UpsertChapterSegment(ctx, sqlc.UpsertChapterSegmentParams{
			FileID: fileID, Kind: string(s.Kind), StartMs: s.Start.Milliseconds(), EndMs: s.End.Milliseconds(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// SetAudioSegment stores a segment found by audio, unless a chapter already gives one of the same
// kind.
func (q Q) SetAudioSegment(ctx context.Context, fileID domain.ID, s domain.Segment) error {
	return q.q.UpsertAudioSegment(ctx, sqlc.UpsertAudioSegmentParams{
		FileID: fileID, Kind: string(s.Kind), StartMs: s.Start.Milliseconds(), EndMs: s.End.Milliseconds(),
	})
}

// SetSegmentScan records that the segment search of a file is done for its current content, with or
// without audio.
func (q Q) SetSegmentScan(ctx context.Context, fileID domain.ID, fingerprint string, audio bool, now time.Time) error {
	return q.q.UpsertSegmentScan(ctx, sqlc.UpsertSegmentScanParams{
		FileID: fileID, Fingerprint: fingerprint, Audio: toInt(audio), ScannedAt: toMillis(now),
	})
}

// SeasonFile is a file of an episode of a season.
type SeasonFile struct {
	ItemID  domain.ID
	Episode int
	FileID  domain.ID
}

// SeasonFiles lists the present, analyzed files of the episodes of a season, by episode number
// (several per episode if it has several versions).
func (q Q) SeasonFiles(ctx context.Context, seasonID domain.ID) ([]SeasonFile, error) {
	rows, err := q.q.ListSeasonEpisodeFiles(ctx, seasonID)
	if err != nil {
		return nil, err
	}
	out := make([]SeasonFile, len(rows))
	for i, r := range rows {
		out[i] = SeasonFile{ItemID: r.ItemID, Episode: int(r.Number), FileID: r.FileID}
	}
	return out, nil
}

// FileEpisode is the episode of a file.
type FileEpisode struct {
	ItemID, SeasonID domain.ID
	Number           int
}

// EpisodeOfFile returns the episode of a file; not found if it is not an episode.
func (q Q) EpisodeOfFile(ctx context.Context, fileID domain.ID) (FileEpisode, error) {
	r, err := q.q.GetFileEpisode(ctx, fileID)
	if err != nil {
		return FileEpisode{}, err
	}
	return FileEpisode{ItemID: r.ItemID, SeasonID: r.SeasonID, Number: int(r.Number)}, nil
}

func (q Q) fileSegments(ctx context.Context, fileID domain.ID) ([]domain.Segment, error) {
	rows, err := q.q.ListFileSegments(ctx, fileID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Segment, len(rows))
	for i, r := range rows {
		out[i] = domain.Segment{
			Kind: domain.SegmentKind(r.Kind), Start: time.Duration(r.StartMs) * time.Millisecond,
			End: time.Duration(r.EndMs) * time.Millisecond, Source: domain.SegmentSource(r.Source),
		}
	}
	return out, nil
}
