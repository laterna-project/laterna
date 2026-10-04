package domain

import "time"

// ItemSort is the order of a catalog list.
type ItemSort string

// Sort orders. Each has one natural direction: title goes A to Z, the others newest or best rated
// first.
const (
	SortTitle    ItemSort = "title"
	SortAdded    ItemSort = "added"
	SortReleased ItemSort = "released"
	SortRating   ItemSort = "rating"
)

// Valid reports a known order.
func (s ItemSort) Valid() bool {
	switch s {
	case SortTitle, SortAdded, SortReleased, SortRating:
		return true
	}
	return false
}

// UserData is what a profile did with an item. For a series or a season, Played is derived from its
// episodes.
type UserData struct {
	Played    bool
	PlayCount int
	// Position is the resume point; 0 means from the start.
	Position     time.Duration
	LastPlayedAt *time.Time
	Favorite     bool
}

// ItemView is an item as a profile sees it: the item, the profile's data on it, the images that can
// be served and, depending on its kind, its place in a series or its child counts.
type ItemView struct {
	Item     Item
	UserData UserData
	// Season is set for a season, Episode (and SeriesTitle) for an episode.
	Season      *Season
	Episode     *Episode
	SeriesTitle string
	// Series and seasons: episodes present, and how many of them the profile has not watched.
	EpisodeCount, UnplayedCount int
	// Track is set for a track. ArtistName is the artist of an album or a track, AlbumTitle the
	// album of a track.
	Track      *Track
	ArtistName string
	AlbumTitle string
	// AlbumCount is the albums of an artist, or the albums inside a photo album. TrackCount is the
	// tracks of an artist or an album.
	AlbumCount, TrackCount int
	// Book is set for a book (SeriesTitle is its series), Reading once the profile has started it.
	Book    *Book
	Reading *ReadingProgress
	// BookCount is the books present in a book series.
	BookCount int
	// Photo is set for a photo. PhotoCount is the photos present in an album.
	Photo      *Photo
	PhotoCount int
	// Images that have been analyzed, so they can be served.
	Images []Image
}

// GenreCount is a genre and how many movies and series carry it.
type GenreCount struct {
	Name  string
	Count int
}

// Resume thresholds.
const (
	// ResumeMinPercent: before this share of the runtime there is nothing to resume, it was just a
	// peek.
	ResumeMinPercent = 5
	// ResumeMaxPercent: past this the item is finished, only the credits are left.
	ResumeMaxPercent = 90
	// ResumeMinPosition: before this there is nothing to resume, whatever the runtime.
	ResumeMinPosition = time.Minute
)

// Progress decides what to make of a playback stopped at position, for a total runtime (0 if
// unknown): resume at the returned position, or mark it finished. Below the minimum the resume
// point is 0. Without a known runtime a playback is never finished.
func Progress(position, runtime time.Duration) (resume time.Duration, finished bool) {
	if runtime > 0 && position >= runtime*ResumeMaxPercent/100 {
		return 0, true
	}
	if position < ResumeMinPosition || (runtime > 0 && position < runtime*ResumeMinPercent/100) {
		return 0, false
	}
	return position, false
}
