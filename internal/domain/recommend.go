package domain

import "time"

// ItemFeatures is what the recommender knows about a movie or a series: whatever can make it close
// to another one.
type ItemFeatures struct {
	ID   ID
	Kind ItemKind
	Year int
	// Rating is the average rating from the NFO (0 to 10), 0 if unknown.
	Rating  float64
	Genres  []string
	Studios []string
	// People are the directors, writers and top-billed actors.
	People []FeaturePerson
	// Collections that contain the item.
	Collections []ID
}

// FeaturePerson is a person and their role on an item.
type FeaturePerson struct {
	ID   ID
	Role PersonRole
}

// TasteSignal is what a profile did with a movie or a series (episodes count for their series):
// time watched, episodes played, played, favorite, started.
type TasteSignal struct {
	ID ID
	// Watched is the time played according to the history, and at least the position reached in a
	// movie that was started, so that playbacks older than the history still count.
	Watched  time.Duration
	Episodes int
	Played   bool
	Favorite bool
	Started  bool
	// LastAt is the last time the profile watched it (zero if unknown).
	LastAt time.Time
}
