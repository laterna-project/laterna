package app

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Playlists: they belong to a profile, are ordered, and the same item may appear more than once.
// What the profile can no longer see (parental control, vanished file) does not show up, but is not
// removed either.

const (
	maxPlaylists       = 100
	maxPlaylistEntries = 2000
	maxPlaylistName    = 100
)

// Playlists lists the playlists of the caller's profile.
func (a *App) Playlists(ctx context.Context, p domain.Principal) ([]domain.PlaylistView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	return a.store.Read().Playlists(ctx, v)
}

// ownPlaylist reads a playlist of the caller's profile (not found otherwise).
func (a *App) ownPlaylist(ctx context.Context, q store.Q, v domain.Viewer, id domain.ID) (domain.Playlist, error) {
	pl, err := q.Playlist(ctx, id)
	if store.IsNotFound(err) || (err == nil && pl.ProfileID != v.ProfileID) {
		return domain.Playlist{}, domain.NotFound("playlist.not_found")
	}
	return pl, err
}

// PlaylistWithEntries returns a playlist and its visible entries, in order.
func (a *App) PlaylistWithEntries(ctx context.Context, p domain.Principal, id domain.ID) (domain.PlaylistView, []domain.PlaylistEntry, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.PlaylistView{}, nil, err
	}
	read := a.store.Read()
	pl, err := a.ownPlaylist(ctx, read, v, id)
	if err != nil {
		return domain.PlaylistView{}, nil, err
	}
	view, err := read.PlaylistView(ctx, v, pl)
	if err != nil {
		return view, nil, err
	}
	entries, err := read.PlaylistEntries(ctx, v, id, 0)
	return view, entries, err
}

// CreatePlaylist creates a playlist with its first items (see AddToPlaylist).
func (a *App) CreatePlaylist(ctx context.Context, p domain.Principal, name string, itemIDs []domain.ID) (domain.PlaylistView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	name = strings.TrimSpace(name)
	if err := validatePlaylistName(name); err != nil {
		return domain.PlaylistView{}, err
	}
	items, err := a.playable(ctx, v, itemIDs)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	now := a.now()
	pl := domain.Playlist{ID: domain.NewID(), ProfileID: v.ProfileID, Name: name, CreatedAt: now, UpdatedAt: now}
	err = a.store.Write(ctx, func(q store.Q) error {
		n, err := q.CountPlaylists(ctx, v.ProfileID)
		if err != nil {
			return err
		}
		if n >= maxPlaylists {
			return domain.Precondition("playlist.too_many", "max", maxPlaylists)
		}
		if err := q.CreatePlaylist(ctx, pl); err != nil {
			return err
		}
		return q.SetPlaylistEntries(ctx, pl.ID, newEntries(items), now)
	})
	if err != nil {
		return domain.PlaylistView{}, err
	}
	return a.store.Read().PlaylistView(ctx, v, pl)
}

// RenamePlaylist renames a playlist.
func (a *App) RenamePlaylist(ctx context.Context, p domain.Principal, id domain.ID, name string) (domain.PlaylistView, error) {
	name = strings.TrimSpace(name)
	if err := validatePlaylistName(name); err != nil {
		return domain.PlaylistView{}, err
	}
	return a.changePlaylist(ctx, p, id, func(q store.Q, pl *domain.Playlist, _ []store.PlaylistEntryRef) ([]store.PlaylistEntryRef, error) {
		pl.Name = name
		return nil, q.RenamePlaylist(ctx, pl.ID, name, a.now())
	})
}

// DeletePlaylist deletes a playlist.
func (a *App) DeletePlaylist(ctx context.Context, p domain.Principal, id domain.ID) error {
	v, err := viewerOf(p)
	if err != nil {
		return err
	}
	return a.store.Write(ctx, func(q store.Q) error {
		if _, err := a.ownPlaylist(ctx, q, v, id); err != nil {
			return err
		}
		return q.DeletePlaylist(ctx, id)
	})
}

// AddToPlaylist adds items to a playlist at the given position (negative or past the end means at
// the end). A movie or an episode is added as is; a season adds its present episodes in order; a
// series adds those of its seasons (without specials).
func (a *App) AddToPlaylist(ctx context.Context, p domain.Principal, id domain.ID, itemIDs []domain.ID, position int) (domain.PlaylistView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	items, err := a.playable(ctx, v, itemIDs)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	return a.changePlaylist(ctx, p, id, func(_ store.Q, _ *domain.Playlist, entries []store.PlaylistEntryRef) ([]store.PlaylistEntryRef, error) {
		if len(entries)+len(items) > maxPlaylistEntries {
			return nil, domain.Precondition("playlist.too_many_entries", "max", maxPlaylistEntries)
		}
		if position < 0 || position > len(entries) {
			position = len(entries)
		}
		return slices.Insert(entries, position, newEntries(items)...), nil
	})
}

// RemoveFromPlaylist removes entries from a playlist.
func (a *App) RemoveFromPlaylist(ctx context.Context, p domain.Principal, id domain.ID, entryIDs []domain.ID) (domain.PlaylistView, error) {
	return a.changePlaylist(ctx, p, id, func(_ store.Q, _ *domain.Playlist, entries []store.PlaylistEntryRef) ([]store.PlaylistEntryRef, error) {
		for _, e := range entryIDs {
			if !slices.ContainsFunc(entries, func(r store.PlaylistEntryRef) bool { return r.ID == e }) {
				return nil, domain.NotFound("playlist.entry_not_found", "entry_id", e)
			}
		}
		return slices.DeleteFunc(entries, func(r store.PlaylistEntryRef) bool { return slices.Contains(entryIDs, r.ID) }), nil
	})
}

// MovePlaylistEntry moves an entry to a new position (clamped to the list).
func (a *App) MovePlaylistEntry(ctx context.Context, p domain.Principal, id, entryID domain.ID, position int) (domain.PlaylistView, error) {
	return a.changePlaylist(ctx, p, id, func(_ store.Q, _ *domain.Playlist, entries []store.PlaylistEntryRef) ([]store.PlaylistEntryRef, error) {
		i := slices.IndexFunc(entries, func(r store.PlaylistEntryRef) bool { return r.ID == entryID })
		if i < 0 {
			return nil, domain.NotFound("playlist.entry_not_found")
		}
		moved := entries[i]
		entries = slices.Delete(entries, i, i+1)
		position = max(0, min(position, len(entries)))
		return slices.Insert(entries, position, moved), nil
	})
}

// changePlaylist changes a playlist of the caller's profile inside a transaction. change gets the
// entries in order and returns the new order (nil leaves it alone).
func (a *App) changePlaylist(ctx context.Context, p domain.Principal, id domain.ID,
	change func(q store.Q, pl *domain.Playlist, entries []store.PlaylistEntryRef) ([]store.PlaylistEntryRef, error),
) (domain.PlaylistView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	var pl domain.Playlist
	err = a.store.Write(ctx, func(q store.Q) error {
		if pl, err = a.ownPlaylist(ctx, q, v, id); err != nil {
			return err
		}
		entries, err := q.PlaylistEntryRefs(ctx, id)
		if err != nil {
			return err
		}
		next, err := change(q, &pl, entries)
		if err != nil || next == nil {
			return err
		}
		return q.SetPlaylistEntries(ctx, id, next, a.now())
	})
	if err != nil {
		return domain.PlaylistView{}, err
	}
	return a.store.Read().PlaylistView(ctx, v, pl)
}

// playable expands items into the movies, episodes and tracks the profile can see: a season into
// its episodes, a series into those of its seasons (without specials), an album into its tracks, an
// artist into those of their albums.
func (a *App) playable(ctx context.Context, v domain.Viewer, itemIDs []domain.ID) ([]domain.ID, error) {
	read := a.store.Read()
	var out []domain.ID
	for _, id := range itemIDs {
		view, err := read.View(ctx, v, id)
		if store.IsNotFound(err) {
			return nil, domain.NotFound("catalog.item_not_found", "item_id", id)
		}
		if err != nil {
			return nil, err
		}
		switch view.Item.Kind {
		case domain.ItemMovie, domain.ItemEpisode, domain.ItemTrack:
			out = append(out, id)
		case domain.ItemAlbum, domain.ItemArtist:
			tracks, err := read.Tracks(ctx, v, view.Item)
			if err != nil {
				return nil, err
			}
			for _, t := range tracks {
				out = append(out, t.Item.ID)
			}
		case domain.ItemSeason, domain.ItemSeries:
			seriesID, season := id, (*domain.ID)(nil)
			if view.Item.Kind == domain.ItemSeason && view.Season != nil {
				seriesID, season = view.Season.SeriesID, &id
			}
			eps, err := read.Episodes(ctx, v, seriesID, season)
			if err != nil {
				return nil, err
			}
			for _, ep := range eps {
				if season == nil && ep.Episode != nil && ep.Episode.SeasonNumber == 0 {
					continue
				}
				out = append(out, ep.Item.ID)
			}
		case domain.ItemBookSeries, domain.ItemBook:
			return nil, domain.Invalid("catalog.not_queueable", "kind", domain.ItemBook)
		case domain.ItemPhotoAlbum, domain.ItemPhoto:
			return nil, domain.Invalid("catalog.not_queueable", "kind", domain.ItemPhoto)
		}
	}
	if len(out) > maxPlaylistEntries {
		return nil, domain.Precondition("playlist.too_many_entries", "max", maxPlaylistEntries)
	}
	return out, nil
}

func newEntries(items []domain.ID) []store.PlaylistEntryRef {
	out := make([]store.PlaylistEntryRef, len(items))
	for i, id := range items {
		out[i] = store.PlaylistEntryRef{ID: domain.NewID(), ItemID: id}
	}
	return out
}

func validatePlaylistName(name string) error {
	switch {
	case name == "":
		return domain.Invalid("playlist.name_required")
	case utf8.RuneCountInString(name) > maxPlaylistName:
		return domain.Invalid("playlist.name_too_long", "max", maxPlaylistName)
	case strings.ContainsFunc(name, unicode.IsControl):
		return domain.Invalid("playlist.name_invalid")
	}
	return nil
}
