package domain

import "time"

// UpcomingKind is what an upcoming release is.
type UpcomingKind string

// Kinds of upcoming releases.
const (
	UpcomingEpisode UpcomingKind = "episode"
	UpcomingMovie   UpcomingKind = "movie"
	UpcomingAlbum   UpcomingKind = "album"
)

// Upcoming is an episode, a movie or an album that Sonarr, Radarr or Lidarr monitors and does not
// have yet, as a profile sees it.
type Upcoming struct {
	Kind UpcomingKind
	// Title of the episode, the movie or the album.
	Title string
	// Parent is the series of an episode or the artist of an album; empty for a movie.
	Parent string
	// Season and Episode number an episode.
	Season, Episode int
	// At is when the episode airs, or midnight UTC of the day a movie or an album comes out.
	At time.Time
	// AllDay is true when At is a day and not a moment.
	AllDay bool
	// ItemID is the series or the artist in the catalog, when the profile sees it there.
	ItemID *ID
	// Images are those of that item.
	Images []Image
	// Poster is the address the instance gives for a poster, for a release without images.
	Poster string
}
