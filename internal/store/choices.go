package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store/sqlc"
)

// Audio and subtitle choices of a profile in a series.

// SeriesChoice reads what a profile last chose in a series; the zero value if nothing yet.
func (q Q) SeriesChoice(ctx context.Context, profile, series domain.ID) (domain.SeriesChoice, error) {
	r, err := q.q.GetSeriesChoice(ctx, sqlc.GetSeriesChoiceParams{ProfileID: profile, SeriesID: series})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SeriesChoice{}, nil
	}
	if err != nil {
		return domain.SeriesChoice{}, err
	}
	return domain.SeriesChoice{
		Audio: r.AudioLanguage, SubtitleChosen: r.SubtitleChosen == 1, Subtitle: r.SubtitleLanguage,
		Forced: r.SubtitleForced == 1, UpdatedAt: fromMillis(r.UpdatedAt),
	}, nil
}

// SetSeriesChoice replaces what a profile last chose in a series.
func (q Q) SetSeriesChoice(ctx context.Context, profile, series domain.ID, c domain.SeriesChoice) error {
	return q.q.UpsertSeriesChoice(ctx, sqlc.UpsertSeriesChoiceParams{
		ProfileID: profile, SeriesID: series, AudioLanguage: c.Audio, SubtitleChosen: toInt(c.SubtitleChosen),
		SubtitleLanguage: c.Subtitle, SubtitleForced: toInt(c.Forced), UpdatedAt: toMillis(c.UpdatedAt),
	})
}
