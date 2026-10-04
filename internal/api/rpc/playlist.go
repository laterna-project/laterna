package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// PlaylistService implements laterna.v1.PlaylistService.
type PlaylistService struct {
	app *app.App
}

func playlistMsg(p domain.PlaylistView) *laternav1.Playlist {
	return &laternav1.Playlist{
		Id: p.ID.String(), Name: p.Name, EntryCount: clampInt32(p.EntryCount), Duration: durationpb.New(p.Duration),
		Images: imagesMsg(p.Images), CreatedAt: timestamppb.New(p.CreatedAt), UpdatedAt: timestamppb.New(p.UpdatedAt),
	}
}

// ListPlaylists lists the playlists of the profile.
func (s *PlaylistService) ListPlaylists(ctx context.Context, _ *connect.Request[laternav1.ListPlaylistsRequest]) (*connect.Response[laternav1.ListPlaylistsResponse], error) {
	list, err := s.app.Playlists(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	resp := &laternav1.ListPlaylistsResponse{}
	for _, p := range list {
		resp.Playlists = append(resp.Playlists, playlistMsg(p))
	}
	return connect.NewResponse(resp), nil
}

// GetPlaylist returns a playlist and its entries.
func (s *PlaylistService) GetPlaylist(ctx context.Context, req *connect.Request[laternav1.GetPlaylistRequest]) (*connect.Response[laternav1.GetPlaylistResponse], error) {
	id, err := parseID(req.Msg.GetPlaylistId(), "playlist_id")
	if err != nil {
		return nil, err
	}
	p, entries, err := s.app.PlaylistWithEntries(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.GetPlaylistResponse{Playlist: playlistMsg(p)}
	for _, e := range entries {
		entry := &laternav1.PlaylistEntry{Id: e.ID.String()}
		switch e.Item.Item.Kind {
		case domain.ItemMovie:
			entry.Item = &laternav1.PlaylistEntry_Movie{Movie: movieSummaryMsg(e.Item)}
		case domain.ItemEpisode:
			entry.Item = &laternav1.PlaylistEntry_Episode{Episode: episodeMsg(ctx, e.Item)}
		case domain.ItemTrack:
			entry.Item = &laternav1.PlaylistEntry_Track{Track: trackMsg(ctx, e.Item)}
		case domain.ItemSeries, domain.ItemSeason, domain.ItemArtist, domain.ItemAlbum, domain.ItemBookSeries, domain.ItemBook,
			domain.ItemPhotoAlbum, domain.ItemPhoto:
			continue
		}
		resp.Entries = append(resp.Entries, entry)
	}
	return connect.NewResponse(resp), nil
}

// CreatePlaylist creates a playlist.
func (s *PlaylistService) CreatePlaylist(ctx context.Context, req *connect.Request[laternav1.CreatePlaylistRequest]) (*connect.Response[laternav1.CreatePlaylistResponse], error) {
	items, err := parseIDs(req.Msg.GetItemIds(), "item_ids")
	if err != nil {
		return nil, err
	}
	p, err := s.app.CreatePlaylist(ctx, principal(ctx), req.Msg.GetName(), items)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.CreatePlaylistResponse{Playlist: playlistMsg(p)}), nil
}

// RenamePlaylist renames a playlist.
func (s *PlaylistService) RenamePlaylist(ctx context.Context, req *connect.Request[laternav1.RenamePlaylistRequest]) (*connect.Response[laternav1.RenamePlaylistResponse], error) {
	id, err := parseID(req.Msg.GetPlaylistId(), "playlist_id")
	if err != nil {
		return nil, err
	}
	p, err := s.app.RenamePlaylist(ctx, principal(ctx), id, req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.RenamePlaylistResponse{Playlist: playlistMsg(p)}), nil
}

// DeletePlaylist deletes a playlist.
func (s *PlaylistService) DeletePlaylist(ctx context.Context, req *connect.Request[laternav1.DeletePlaylistRequest]) (*connect.Response[laternav1.DeletePlaylistResponse], error) {
	id, err := parseID(req.Msg.GetPlaylistId(), "playlist_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.DeletePlaylist(ctx, principal(ctx), id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DeletePlaylistResponse{}), nil
}

// AddToPlaylist adds items to a playlist.
func (s *PlaylistService) AddToPlaylist(ctx context.Context, req *connect.Request[laternav1.AddToPlaylistRequest]) (*connect.Response[laternav1.AddToPlaylistResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetPlaylistId(), "playlist_id")
	if err != nil {
		return nil, err
	}
	items, err := parseIDs(m.GetItemIds(), "item_ids")
	if err != nil {
		return nil, err
	}
	position := -1
	if m.Position != nil {
		position = int(m.GetPosition())
	}
	p, err := s.app.AddToPlaylist(ctx, principal(ctx), id, items, position)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.AddToPlaylistResponse{Playlist: playlistMsg(p)}), nil
}

// RemoveFromPlaylist removes entries from a playlist.
func (s *PlaylistService) RemoveFromPlaylist(ctx context.Context, req *connect.Request[laternav1.RemoveFromPlaylistRequest]) (*connect.Response[laternav1.RemoveFromPlaylistResponse], error) {
	id, err := parseID(req.Msg.GetPlaylistId(), "playlist_id")
	if err != nil {
		return nil, err
	}
	entries, err := parseIDs(req.Msg.GetEntryIds(), "entry_ids")
	if err != nil {
		return nil, err
	}
	p, err := s.app.RemoveFromPlaylist(ctx, principal(ctx), id, entries)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.RemoveFromPlaylistResponse{Playlist: playlistMsg(p)}), nil
}

// MovePlaylistEntry moves an entry.
func (s *PlaylistService) MovePlaylistEntry(ctx context.Context, req *connect.Request[laternav1.MovePlaylistEntryRequest]) (*connect.Response[laternav1.MovePlaylistEntryResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetPlaylistId(), "playlist_id")
	if err != nil {
		return nil, err
	}
	entry, err := parseID(m.GetEntryId(), "entry_id")
	if err != nil {
		return nil, err
	}
	p, err := s.app.MovePlaylistEntry(ctx, principal(ctx), id, entry, int(m.GetPosition()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.MovePlaylistEntryResponse{Playlist: playlistMsg(p)}), nil
}
