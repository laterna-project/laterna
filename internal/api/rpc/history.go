package rpc

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// HistoryService implements laterna.v1.HistoryService.
type HistoryService struct {
	app *app.App
}

// ListHistory lists the plays of the profile.
func (s *HistoryService) ListHistory(ctx context.Context, req *connect.Request[laternav1.ListHistoryRequest]) (*connect.Response[laternav1.ListHistoryResponse], error) {
	page, err := s.app.History(ctx, principal(ctx), req.Msg.GetPageToken(), int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListHistoryResponse{NextPageToken: page.NextPageToken}
	for _, e := range page.Entries {
		resp.Entries = append(resp.Entries, historyEntryMsg(ctx, e.Play, e.View))
	}
	return connect.NewResponse(resp), nil
}

// DeleteHistoryEntry deletes a play.
func (s *HistoryService) DeleteHistoryEntry(ctx context.Context, req *connect.Request[laternav1.DeleteHistoryEntryRequest]) (*connect.Response[laternav1.DeleteHistoryEntryResponse], error) {
	id, err := parseID(req.Msg.GetEntryId(), "entry_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeleteHistoryEntry(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeleteHistoryEntryResponse{}), nil
}

// ClearHistory wipes the profile's history.
func (s *HistoryService) ClearHistory(ctx context.Context, _ *connect.Request[laternav1.ClearHistoryRequest]) (*connect.Response[laternav1.ClearHistoryResponse], error) {
	n, err := s.app.ClearHistory(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ClearHistoryResponse{Deleted: n}), nil
}

// GetStats sums up the history of one year, or of all time.
func (s *HistoryService) GetStats(ctx context.Context, req *connect.Request[laternav1.GetStatsRequest]) (*connect.Response[laternav1.GetStatsResponse], error) {
	st, err := s.app.Stats(ctx, principal(ctx), int(req.Msg.GetYear()), req.Msg.GetTimeZone())
	if err != nil {
		return nil, err
	}
	msg := &laternav1.Stats{
		Total: durationpb.New(st.Total), MoviesTime: durationpb.New(st.MoviesTime), EpisodesTime: durationpb.New(st.EpisodesTime),
		MusicTime: durationpb.New(st.MusicTime), Plays: clampInt32(st.Plays), Movies: clampInt32(st.Movies),
		Episodes: clampInt32(st.Episodes), Series: clampInt32(st.Series), Tracks: clampInt32(st.Tracks),
		TopSeries: statEntries(st.TopSeries), TopMovies: statEntries(st.TopMovies), TopArtists: statEntries(st.TopArtists),
		TopTracks: statEntries(st.TopTracks), TopGenres: statEntries(st.TopGenres),
	}
	for _, b := range st.Timeline {
		msg.Timeline = append(msg.Timeline, timeBucketMsg(b))
	}
	for _, d := range st.ByHour {
		msg.ByHour = append(msg.ByHour, durationpb.New(d))
	}
	if st.BusiestDay != nil {
		msg.BusiestDay = timeBucketMsg(*st.BusiestDay)
	}
	if b := st.Binge; b != nil {
		msg.Binge = &laternav1.Binge{
			SeriesId: idString(b.SeriesID), Series: b.Series, Day: timestamppb.New(b.Day), Episodes: clampInt32(b.Episodes),
			Time: durationpb.New(b.Time),
		}
	}
	if st.First != nil {
		msg.First = historyEntryMsg(ctx, *st.First, nil)
	}
	if st.Last != nil {
		msg.Last = historyEntryMsg(ctx, *st.Last, nil)
	}
	return connect.NewResponse(&laternav1.GetStatsResponse{Stats: msg}), nil
}

var historyKinds = map[domain.ItemKind]laternav1.HistoryKind{
	domain.ItemMovie:   laternav1.HistoryKind_HISTORY_KIND_MOVIE,
	domain.ItemEpisode: laternav1.HistoryKind_HISTORY_KIND_EPISODE,
	domain.ItemTrack:   laternav1.HistoryKind_HISTORY_KIND_TRACK,
}

// historyEntryMsg converts a play, with the current card of its item if view is not nil.
func historyEntryMsg(ctx context.Context, p domain.Play, view *domain.ItemView) *laternav1.HistoryEntry {
	msg := &laternav1.HistoryEntry{
		Id: p.ID.String(), Kind: historyKinds[p.Kind], Title: p.Title, Subtitle: p.Subtitle,
		StartedAt: timestamppb.New(p.StartedAt), EndedAt: timestamppb.New(p.EndedAt),
		Watched: durationpb.New(p.Watched), Position: durationpb.New(p.Position), Completed: p.Completed,
		Device: p.Device, Offline: p.Offline,
	}
	if view != nil {
		switch view.Item.Kind {
		case domain.ItemMovie:
			msg.Item = &laternav1.HistoryEntry_Movie{Movie: movieSummaryMsg(*view)}
		case domain.ItemEpisode:
			msg.Item = &laternav1.HistoryEntry_Episode{Episode: episodeMsg(ctx, *view)}
		case domain.ItemTrack:
			msg.Item = &laternav1.HistoryEntry_Track{Track: trackMsg(ctx, *view)}
		case domain.ItemSeries, domain.ItemSeason, domain.ItemArtist, domain.ItemAlbum, domain.ItemBookSeries, domain.ItemBook,
			domain.ItemPhotoAlbum, domain.ItemPhoto:
		}
	}
	return msg
}

func statEntries(entries []domain.StatEntry) []*laternav1.StatEntry {
	out := make([]*laternav1.StatEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, &laternav1.StatEntry{
			ItemId: idString(e.ID), Name: e.Name, Time: durationpb.New(e.Time), Plays: clampInt32(e.Plays), Images: imagesMsg(e.Images),
		})
	}
	return out
}

func timeBucketMsg(b domain.TimeBucket) *laternav1.TimeBucket {
	return &laternav1.TimeBucket{Start: timestamppb.New(b.Start), Time: durationpb.New(b.Time.Round(time.Second))}
}

func idString(id *domain.ID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
