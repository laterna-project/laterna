package rpc

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// HomeService implements laterna.v1.HomeService.
type HomeService struct {
	app *app.App
}

var homeRowKinds = map[app.HomeRowKind]laternav1.HomeRowKind{
	app.RowResume:            laternav1.HomeRowKind_HOME_ROW_KIND_RESUME,
	app.RowRecommended:       laternav1.HomeRowKind_HOME_ROW_KIND_RECOMMENDED,
	app.RowBecauseYouWatched: laternav1.HomeRowKind_HOME_ROW_KIND_BECAUSE_YOU_WATCHED,
	app.RowNextUp:            laternav1.HomeRowKind_HOME_ROW_KIND_NEXT_UP,
	app.RowLatestMovies:      laternav1.HomeRowKind_HOME_ROW_KIND_LATEST_MOVIES,
	app.RowLatestSeries:      laternav1.HomeRowKind_HOME_ROW_KIND_LATEST_SERIES,
	app.RowRecentAlbums:      laternav1.HomeRowKind_HOME_ROW_KIND_RECENT_ALBUMS,
	app.RowLatestAlbums:      laternav1.HomeRowKind_HOME_ROW_KIND_LATEST_ALBUMS,
	app.RowReading:           laternav1.HomeRowKind_HOME_ROW_KIND_READING,
	app.RowLatestBooks:       laternav1.HomeRowKind_HOME_ROW_KIND_LATEST_BOOKS,
	app.RowLatestPhotos:      laternav1.HomeRowKind_HOME_ROW_KIND_LATEST_PHOTOS,
}

// GetHome returns the home page of the profile.
func (s *HomeService) GetHome(ctx context.Context, req *connect.Request[laternav1.GetHomeRequest]) (*connect.Response[laternav1.GetHomeResponse], error) {
	rows, err := s.app.Home(ctx, principal(ctx), int(req.Msg.GetRowSize()))
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.HomeRow, len(rows))
	for i, r := range rows {
		msg := &laternav1.HomeRow{Kind: homeRowKinds[r.Kind], Title: render(ctx, r.Title), TitleText: textMsg(ctx, r.Title)}
		if r.Library != nil {
			msg.LibraryId = r.Library.ID.String()
		}
		msg.SourceItemId = idString(r.Source)
		for _, v := range r.Items {
			switch v.Item.Kind {
			case domain.ItemMovie:
				msg.Items = append(msg.Items, &laternav1.HomeItem{Item: &laternav1.HomeItem_Movie{Movie: movieSummaryMsg(v)}})
			case domain.ItemSeries:
				msg.Items = append(msg.Items, &laternav1.HomeItem{Item: &laternav1.HomeItem_Series{Series: seriesSummaryMsg(v)}})
			case domain.ItemEpisode:
				msg.Items = append(msg.Items, &laternav1.HomeItem{Item: &laternav1.HomeItem_Episode{Episode: episodeMsg(ctx, v)}})
			case domain.ItemAlbum:
				msg.Items = append(msg.Items, &laternav1.HomeItem{Item: &laternav1.HomeItem_Album{Album: albumSummaryMsg(ctx, v)}})
			case domain.ItemBook:
				msg.Items = append(msg.Items, &laternav1.HomeItem{Item: &laternav1.HomeItem_Book{Book: bookSummaryMsg(ctx, v)}})
			case domain.ItemPhoto:
				msg.Items = append(msg.Items, &laternav1.HomeItem{Item: &laternav1.HomeItem_Photo{Photo: photoSummaryMsg(v)}})
			case domain.ItemSeason, domain.ItemArtist, domain.ItemTrack, domain.ItemBookSeries, domain.ItemPhotoAlbum:
			}
		}
		out[i] = msg
	}
	return connect.NewResponse(&laternav1.GetHomeResponse{Rows: out}), nil
}

// EventService implements laterna.v1.EventService.
type EventService struct {
	app *app.App
}

// heartbeatEvery is the time between keep-alive messages on a stream without events.
const heartbeatEvery = 30 * time.Second

// Subscribe sends the events meant for the caller until the stream is closed (client gone, or
// server shutdown: the request context is then canceled).
func (s *EventService) Subscribe(ctx context.Context, _ *connect.Request[laternav1.SubscribeRequest], stream *connect.ServerStream[laternav1.SubscribeResponse]) error {
	sub := s.app.Subscribe(principal(ctx))
	defer sub.Close()
	heartbeat := &laternav1.Event{Kind: &laternav1.Event_Heartbeat{Heartbeat: &laternav1.Heartbeat{}}}
	// First message right away, so the client knows the stream is up.
	if err := send(stream, heartbeat); err != nil {
		return err
	}
	for {
		wait, cancel := context.WithTimeout(ctx, heartbeatEvery)
		e, err := sub.Next(wait)
		cancel()
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, context.DeadlineExceeded):
			err = send(stream, heartbeat)
		case err != nil:
			return err
		default:
			err = send(stream, eventMsg(e))
		}
		if err != nil {
			return err
		}
	}
}

func send(stream *connect.ServerStream[laternav1.SubscribeResponse], e *laternav1.Event) error {
	e.Time = timestamppb.Now()
	return stream.Send(&laternav1.SubscribeResponse{Event: e})
}

func idsMsg(ids []domain.ID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func eventMsg(e domain.Event) *laternav1.Event {
	switch e := e.(type) {
	case domain.Resync:
		return &laternav1.Event{Kind: &laternav1.Event_Resync{Resync: &laternav1.Resync{}}}
	case domain.LibrariesChanged:
		return &laternav1.Event{Kind: &laternav1.Event_LibrariesChanged{LibrariesChanged: &laternav1.LibrariesChanged{}}}
	case domain.LibraryScanned:
		return &laternav1.Event{Kind: &laternav1.Event_LibraryScanned{LibraryScanned: &laternav1.LibraryScanned{
			LibraryId: e.LibraryID.String(), Files: clampInt32(e.Files), Added: clampInt32(e.Added),
			Changed: clampInt32(e.Changed), Moved: clampInt32(e.Moved), Missing: clampInt32(e.Missing),
			Returned: clampInt32(e.Returned), Forgotten: clampInt32(e.Forgotten),
		}}}
	case domain.ItemsChanged:
		return &laternav1.Event{Kind: &laternav1.Event_ItemsChanged{ItemsChanged: &laternav1.ItemsChanged{
			LibraryId: e.LibraryID.String(), ItemIds: idsMsg(e.ItemIDs), Truncated: e.Truncated,
		}}}
	case domain.UserDataChanged:
		return &laternav1.Event{Kind: &laternav1.Event_UserDataChanged{UserDataChanged: &laternav1.UserDataChanged{
			ItemIds: idsMsg(e.ItemIDs), Truncated: e.Truncated,
		}}}
	case domain.DownloadsChanged:
		return &laternav1.Event{Kind: &laternav1.Event_DownloadsChanged{DownloadsChanged: &laternav1.DownloadsChanged{
			DownloadIds: idsMsg(e.DownloadIDs),
		}}}
	case domain.ThemesChanged:
		return &laternav1.Event{Kind: &laternav1.Event_ThemesChanged{ThemesChanged: &laternav1.ThemesChanged{}}}
	}
	// Unknown kind (cannot happen: the interface is sealed and checked by gochecksumtype): reload
	// everything.
	return &laternav1.Event{Kind: &laternav1.Event_Resync{Resync: &laternav1.Resync{}}}
}
