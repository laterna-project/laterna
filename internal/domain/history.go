package domain

import "time"

// Play is one playback by a profile: a movie, an episode or a track, from the moment playback opens
// until it stops.
type Play struct {
	ID        ID
	ProfileID ID
	// ItemID is the item played, nil if it has been forgotten since. The title stays.
	ItemID *ID
	Kind   ItemKind
	// SeriesID is the series of an episode. AlbumID and ArtistID are the album of a track and its
	// artist.
	SeriesID, AlbumID, ArtistID *ID
	// Title of the item. Subtitle is the series of an episode or the artists of a track.
	Title, Subtitle    string
	StartedAt, EndedAt time.Time
	// Watched is the time actually played, without seeks and pauses. Position is where playback
	// stopped. Duration is the item's runtime.
	Watched, Position, Duration time.Duration
	Completed                   bool
	Device                      string
	// Offline means played offline and reported later; Watched is then the position reached.
	Offline bool
}

// Below these a playback is a peek or a false start and stays out of the history.
const (
	MinWatchedVideo = time.Minute
	MinWatchedTrack = 30 * time.Second
)

// CountsAsPlay reports whether a playback goes into the history: enough was played, or at least
// half of an item shorter than the threshold.
func CountsAsPlay(kind ItemKind, watched, duration time.Duration) bool {
	least := MinWatchedVideo
	if kind == ItemTrack {
		least = MinWatchedTrack
	}
	if duration > 0 {
		least = min(least, duration/2)
	}
	return watched > 0 && watched >= least
}

// Stats sums up a profile's history over a period: one year, or all time. Days, hours and months
// are in the client's time zone, taken at the start of each play.
type Stats struct {
	// Total time played, then per kind.
	Total, MoviesTime, EpisodesTime, MusicTime time.Duration
	// Plays is the number of playbacks. Movies, Episodes and Tracks count distinct items. Series
	// counts series with at least one episode watched.
	Plays, Movies, Episodes, Series, Tracks int
	// Rankings, most played first (five at most).
	TopSeries, TopMovies, TopArtists, TopTracks, TopGenres []StatEntry
	// Timeline is the time played per month of a year, or per year for all time.
	Timeline []TimeBucket
	// ByHour is the time played per weekday and hour, starting Monday at midnight (7 x 24).
	ByHour [7 * 24]time.Duration
	// BusiestDay is the day with the most time played, nil without any play.
	BusiestDay *TimeBucket
	// Binge is the most episodes of one series watched in a day, nil without any episode.
	Binge *Binge
	// First and Last plays of the period.
	First, Last *Play
}

// StatEntry is one line of a ranking: a series, a movie, an artist, a track or a genre.
type StatEntry struct {
	// ID of the item, nil for a genre or a forgotten item.
	ID    *ID
	Name  string
	Time  time.Duration
	Plays int
	// Images of the item if it still exists (poster, cover, artist photo).
	Images []Image
}

// TimeBucket is the time played over a period starting at Start (midnight, local time).
type TimeBucket struct {
	Start time.Time
	Time  time.Duration
}

// Binge says that Episodes distinct episodes of a series were watched on Day.
type Binge struct {
	SeriesID *ID
	Series   string
	Day      time.Time
	Episodes int
	Time     time.Duration
}
