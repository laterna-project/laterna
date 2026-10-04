package domain

import "time"

// Collection groups movies and series: a franchise read from NFO files (the <set> Radarr writes) or
// a selection made by an administrator. Collections are shared by the whole server.
type Collection struct {
	ID       ID
	Name     string
	Overview string
	// Manual means created by an administrator; otherwise it follows the NFO files.
	Manual    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CollectionView is a collection as a profile sees it.
type CollectionView struct {
	Collection
	// ItemCount counts the movies and series the profile can see.
	ItemCount int
	// Images are the posters of the first items (four at most), to build a cover.
	Images []Image
}

// Playlist belongs to a profile: movies and episodes in a chosen order. The same item may appear
// more than once.
type Playlist struct {
	ID        ID
	ProfileID ID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PlaylistView is a playlist with what the profile can see of it.
type PlaylistView struct {
	Playlist
	EntryCount int
	// Duration is the total runtime of the entries.
	Duration time.Duration
	// Images are the posters or thumbs of the first entries (four at most).
	Images []Image
}

// PlaylistEntry is one entry of a playlist.
type PlaylistEntry struct {
	ID   ID
	Item ItemView
}
