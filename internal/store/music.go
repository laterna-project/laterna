package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Music: what is specific to tracks. Artists and albums are plain items (an album points to its
// artist through parent_id).

// SetTrack stores (or updates) the track-specific part of an item.
func (q Q) SetTrack(ctx context.Context, t domain.Track) error {
	return translate(q.q.UpsertTrack(ctx, sqlc.UpsertTrackParams{
		ItemID: t.ItemID, AlbumID: t.AlbumID, ArtistID: t.ArtistID, Disc: int64(t.Disc), Number: int64(t.Number),
		Artists: t.Artists, TrackGain: nullFloat(t.TrackGain), TrackPeak: nullFloat(t.TrackPeak),
		AlbumGain: nullFloat(t.AlbumGain), AlbumPeak: nullFloat(t.AlbumPeak),
	}))
}

// Track reads the track-specific part of an item.
func (q Q) Track(ctx context.Context, itemID domain.ID) (domain.Track, error) {
	r, err := q.q.GetTrack(ctx, itemID)
	if err != nil {
		return domain.Track{}, err
	}
	return trackFromRow(r), nil
}

func trackFromRow(r sqlc.Track) domain.Track {
	return domain.Track{
		ItemID: r.ItemID, AlbumID: r.AlbumID, ArtistID: r.ArtistID, Disc: int(r.Disc), Number: int(r.Number),
		Artists: r.Artists, TrackGain: optFloat(r.TrackGain), TrackPeak: optFloat(r.TrackPeak),
		AlbumGain: optFloat(r.AlbumGain), AlbumPeak: optFloat(r.AlbumPeak),
	}
}

// AlbumTrackIDs lists the tracks of an album (present or not), in order.
func (q Q) AlbumTrackIDs(ctx context.Context, albumID domain.ID) ([]domain.ID, error) {
	return q.q.ListAlbumTrackIDs(ctx, albumID)
}

// ArtistTrackIDs lists the tracks of an artist's albums (present or not), album by album.
func (q Q) ArtistTrackIDs(ctx context.Context, artistID domain.ID) ([]domain.ID, error) {
	return q.q.ListArtistTrackIDs(ctx, artistID)
}

// FileRef names a file: ID and path.
type FileRef struct {
	ID   domain.ID
	Path string
}

// FirstFileOfArtist returns the first present file of an artist (from their oldest album).
func (q Q) FirstFileOfArtist(ctx context.Context, artistID domain.ID) (FileRef, error) {
	r, err := q.q.FirstFileOfArtist(ctx, artistID)
	return FileRef{ID: r.ID, Path: r.Path}, err
}

// AlbumFiles lists the present files of an album, in track order.
func (q Q) AlbumFiles(ctx context.Context, albumID domain.ID) ([]FileRef, error) {
	rows, err := q.q.ListAlbumFiles(ctx, albumID)
	if err != nil {
		return nil, err
	}
	out := make([]FileRef, len(rows))
	for i, r := range rows {
		out[i] = FileRef{ID: r.ID, Path: r.Path}
	}
	return out, nil
}

// AlbumSummary is what the present tracks of an album say about it: total duration, most recent
// year, genres from most to least frequent.
type AlbumSummary struct {
	Runtime time.Duration
	Year    int
	Genres  []string
}

// AlbumTracks sums up the present tracks of an album.
func (q Q) AlbumTracks(ctx context.Context, albumID domain.ID) (AlbumSummary, error) {
	r, err := q.q.AlbumTrackSummary(ctx, albumID)
	if err != nil {
		return AlbumSummary{}, err
	}
	genres, err := q.q.ListAlbumTrackGenres(ctx, albumID)
	if err != nil {
		return AlbumSummary{}, err
	}
	return AlbumSummary{Runtime: time.Duration(r.RuntimeMs) * time.Millisecond, Year: int(r.Year), Genres: genres}, nil
}

func nullFloat(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *f, Valid: true}
}

func optFloat(f sql.NullFloat64) *float64 {
	if !f.Valid {
		return nil
	}
	return &f.Float64
}
