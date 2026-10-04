package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// MusicService implements laterna.v1.MusicService.
type MusicService struct {
	app *app.App
}

// ListArtists returns a page of artists.
func (s *MusicService) ListArtists(ctx context.Context, req *connect.Request[laternav1.ListArtistsRequest]) (*connect.Response[laternav1.ListArtistsResponse], error) {
	m := req.Msg
	lib, err := parseOptionalID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	page, err := s.app.ListArtists(ctx, principal(ctx), app.ListQuery{
		LibraryID: lib, Sort: itemSortFromMsg(m.GetSort()), Reverse: m.GetReverse(),
		FavoritesOnly: m.GetFavoritesOnly(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.ArtistSummary, len(page.Items))
	for i, v := range page.Items {
		out[i] = artistSummaryMsg(ctx, v)
	}
	return connect.NewResponse(&laternav1.ListArtistsResponse{
		Artists: out, NextPageToken: page.NextPageToken, TotalSize: clampInt32(page.Total),
	}), nil
}

// GetArtist returns the details of an artist and their albums.
func (s *MusicService) GetArtist(ctx context.Context, req *connect.Request[laternav1.GetArtistRequest]) (*connect.Response[laternav1.GetArtistResponse], error) {
	id, err := parseID(req.Msg.GetArtistId(), "artist_id")
	if err != nil {
		return nil, err
	}
	v, d, albums, err := s.app.Artist(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	it := v.Item
	summary := artistSummaryMsg(ctx, v)
	resp := &laternav1.GetArtistResponse{Artist: &laternav1.Artist{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Name: summary.GetName(), NameText: summary.GetNameText(),
		Overview: it.Overview, Genres: d.Genres,
		ProviderIds: d.ProviderIDs, Images: imagesMsg(v.Images), AlbumCount: clampInt32(v.AlbumCount),
		TrackCount: clampInt32(v.TrackCount), UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
	}}
	for _, a := range albums {
		resp.Albums = append(resp.Albums, albumSummaryMsg(ctx, a))
	}
	return connect.NewResponse(resp), nil
}

// ListAlbums returns a page of albums.
func (s *MusicService) ListAlbums(ctx context.Context, req *connect.Request[laternav1.ListAlbumsRequest]) (*connect.Response[laternav1.ListAlbumsResponse], error) {
	m := req.Msg
	lib, err := parseOptionalID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	artist, err := parseOptionalID(m.GetArtistId(), "artist_id")
	if err != nil {
		return nil, err
	}
	page, err := s.app.ListAlbums(ctx, principal(ctx), app.ListQuery{
		LibraryID: lib, ArtistID: artist, Sort: itemSortFromMsg(m.GetSort()), Reverse: m.GetReverse(), Genre: m.GetGenre(),
		FavoritesOnly: m.GetFavoritesOnly(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.AlbumSummary, len(page.Items))
	for i, v := range page.Items {
		out[i] = albumSummaryMsg(ctx, v)
	}
	return connect.NewResponse(&laternav1.ListAlbumsResponse{
		Albums: out, NextPageToken: page.NextPageToken, TotalSize: clampInt32(page.Total),
	}), nil
}

// GetAlbum returns the details of an album and its tracks.
func (s *MusicService) GetAlbum(ctx context.Context, req *connect.Request[laternav1.GetAlbumRequest]) (*connect.Response[laternav1.GetAlbumResponse], error) {
	id, err := parseID(req.Msg.GetAlbumId(), "album_id")
	if err != nil {
		return nil, err
	}
	v, d, tracks, err := s.app.Album(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	summary := albumSummaryMsg(ctx, v)
	it := v.Item
	resp := &laternav1.GetAlbumResponse{Album: &laternav1.Album{
		Id: summary.GetId(), LibraryId: summary.GetLibraryId(), Title: summary.GetTitle(), TitleText: summary.GetTitleText(),
		ArtistId: summary.GetArtistId(), ArtistName: summary.GetArtistName(), Year: clampInt32(it.Year), PremiereDate: it.PremiereDate, Overview: it.Overview,
		Genres: d.Genres, ProviderIds: d.ProviderIDs, Images: summary.GetImages(), TrackCount: clampInt32(v.TrackCount),
		Runtime: durationMsg(it.Runtime), UserData: summary.GetUserData(), AddedAt: summary.GetAddedAt(),
	}}
	for _, t := range tracks {
		resp.Tracks = append(resp.Tracks, trackMsg(ctx, t))
	}
	return connect.NewResponse(resp), nil
}

// ListTracks returns a page of tracks.
func (s *MusicService) ListTracks(ctx context.Context, req *connect.Request[laternav1.ListTracksRequest]) (*connect.Response[laternav1.ListTracksResponse], error) {
	m := req.Msg
	lib, err := parseOptionalID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	page, err := s.app.ListTracks(ctx, principal(ctx), app.ListQuery{
		LibraryID: lib, Sort: itemSortFromMsg(m.GetSort()), Reverse: m.GetReverse(),
		FavoritesOnly: m.GetFavoritesOnly(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ListTracksResponse{
		Tracks: tracksMsg(ctx, page.Items), NextPageToken: page.NextPageToken, TotalSize: clampInt32(page.Total),
	}), nil
}

// GetTrack returns a track and its files.
func (s *MusicService) GetTrack(ctx context.Context, req *connect.Request[laternav1.GetTrackRequest]) (*connect.Response[laternav1.GetTrackResponse], error) {
	id, err := parseID(req.Msg.GetTrackId(), "track_id")
	if err != nil {
		return nil, err
	}
	v, d, err := s.app.Track(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.GetTrackResponse{Track: trackMsg(ctx, v), Files: filesMsg(d.Files)}), nil
}

// ListArtistTracks returns all the tracks of an artist.
func (s *MusicService) ListArtistTracks(ctx context.Context, req *connect.Request[laternav1.ListArtistTracksRequest]) (*connect.Response[laternav1.ListArtistTracksResponse], error) {
	id, err := parseID(req.Msg.GetArtistId(), "artist_id")
	if err != nil {
		return nil, err
	}
	tracks, err := s.app.ArtistTracks(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.ListArtistTracksResponse{Tracks: tracksMsg(ctx, tracks)}), nil
}

func tracksMsg(ctx context.Context, views []domain.ItemView) []*laternav1.Track {
	out := make([]*laternav1.Track, len(views))
	for i, v := range views {
		out[i] = trackMsg(ctx, v)
	}
	return out
}
