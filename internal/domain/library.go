package domain

import "time"

// LibraryKind decides how the files of a library are understood.
type LibraryKind string

// Library kinds.
const (
	LibraryMovies LibraryKind = "movies"
	LibraryShows  LibraryKind = "shows"
	LibraryMusic  LibraryKind = "music"
	LibraryBooks  LibraryKind = "books"
	LibraryPhotos LibraryKind = "photos"
)

// Valid reports a known kind.
func (k LibraryKind) Valid() bool {
	return k == LibraryMovies || k == LibraryShows || k == LibraryMusic || k == LibraryBooks || k == LibraryPhotos
}

// Library is a set of folders of the same kind.
type Library struct {
	ID    ID
	Name  string
	Kind  LibraryKind
	Paths []string
	// Language of the metadata (BCP 47, "en-US").
	Language string
	// Position is the rank given by the administrator (1, 2, 3...). 0 means none: the library comes
	// after the ranked ones, by kind then by name.
	Position   int
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastScanAt *time.Time
}

// ItemKind is the kind of a catalog item.
type ItemKind string

// Item kinds.
const (
	ItemMovie   ItemKind = "movie"
	ItemSeries  ItemKind = "series"
	ItemSeason  ItemKind = "season"
	ItemEpisode ItemKind = "episode"
	// Music: the artist of an album, the album, the track.
	ItemArtist ItemKind = "artist"
	ItemAlbum  ItemKind = "album"
	ItemTrack  ItemKind = "track"
	// Books: the series and the book (one volume).
	ItemBookSeries ItemKind = "book_series"
	ItemBook       ItemKind = "book"
	// Photos: the album (a folder, inside its parent album) and the photo.
	ItemPhotoAlbum ItemKind = "photo_album"
	ItemPhoto      ItemKind = "photo"
)

// Music reports one of the music kinds.
func (k ItemKind) Music() bool { return k == ItemArtist || k == ItemAlbum || k == ItemTrack }

// Item holds what every catalog item has.
type Item struct {
	ID        ID
	LibraryID ID
	Kind      ItemKind
	// ParentID is the series of a season, the season of an episode, the artist of an album, the
	// album of a track, the series of a book, or the album of a photo or of a nested photo album.
	ParentID *ID
	// GroupKey groups the files of one item (folder, title, numbers).
	GroupKey      string
	Title         string
	SortTitle     string
	OriginalTitle string
	Year          int
	// PremiereDate as YYYY-MM-DD, empty if unknown.
	PremiereDate    string
	Overview        string
	Tagline         string
	OfficialRating  string
	CommunityRating float64
	Runtime         time.Duration
	MetadataAt      *time.Time
	AddedAt         time.Time
	UpdatedAt       time.Time
}

// Season is the season-specific part of an item.
type Season struct {
	ItemID   ID
	SeriesID ID
	Number   int
}

// Episode is the episode-specific part of an item.
type Episode struct {
	ItemID       ID
	SeriesID     ID
	SeasonID     ID
	SeasonNumber int
	Number       int
	// NumberEnd is the last episode of a multi-episode file, 0 otherwise.
	NumberEnd int
	Absolute  bool
}

// Track is the track-specific part of an item.
type Track struct {
	ItemID ID
	// AlbumID is the track's album, ArtistID that album's artist.
	AlbumID  ID
	ArtistID ID
	// Disc and Number give the disc and the position on it; 0 if unknown.
	Disc   int
	Number int
	// Artists are the track artists as the tags spell them ("Daft Punk", "Stardust feat. Benjamin
	// Diamond").
	Artists string
	// ReplayGain: gain in dB and peak (1 is full scale) for the track and for its album. Nil when
	// the tags do not say.
	TrackGain, TrackPeak, AlbumGain, AlbumPeak *float64
}

// PersonRole is what a person did on an item.
type PersonRole string

// Roles.
const (
	RoleActor    PersonRole = "actor"
	RoleDirector PersonRole = "director"
	RoleWriter   PersonRole = "writer"
	// RoleIllustrator is the artist of a comic.
	RoleIllustrator PersonRole = "illustrator"
)

// Credit links a person to an item.
type Credit struct {
	PersonID  ID
	Name      string
	Role      PersonRole
	Character string
	Order     int
	// Thumb is the photo URL given by the NFO (set when writing).
	Thumb string
	// Image is the photo once it is ready (set when reading).
	Image *Image
}

// Metadata is everything known about an item, written in one go.
type Metadata struct {
	Title          string
	SortTitle      string
	OriginalTitle  string
	Year           int
	PremiereDate   string
	Overview       string
	Tagline        string
	OfficialRating string
	// AgeRating is the age derived from OfficialRating (see RatingAge); nil means unrated.
	AgeRating       *int
	CommunityRating float64
	Runtime         time.Duration
	Genres          []string
	Studios         []string
	// ProviderIDs are the IDs at metadata providers (tmdb, imdb, tvdb).
	ProviderIDs map[string]string
	Credits     []Credit
}
