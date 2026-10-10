package domain

import (
	"strconv"
	"time"
)

// Requests for movies and series (docs/design/requests.md).

// RequestKind is what is requested: a series through Sonarr, a movie through Radarr, an artist or
// an album through Lidarr, a book through LazyLibrarian. RequestMusic is the family of artists and
// albums: what a search or a destination is for (Family).
type RequestKind string

// Request kinds.
const (
	RequestSeries RequestKind = "series"
	RequestMovie  RequestKind = "movie"
	RequestMusic  RequestKind = "music"
	RequestArtist RequestKind = "artist"
	RequestAlbum  RequestKind = "album"
	RequestBook   RequestKind = "book"
)

// RequestStatus is where a request stands.
type RequestStatus string

// Request states, in the order a request goes through them.
const (
	RequestPending     RequestStatus = "pending"
	RequestApproved    RequestStatus = "approved"
	RequestDownloading RequestStatus = "downloading"
	RequestAvailable   RequestStatus = "available"
	RequestDeclined    RequestStatus = "declined"
	RequestFailed      RequestStatus = "failed"
)

// Open means the request is on its way: a second one for the same title is refused.
func (s RequestStatus) Open() bool {
	return s == RequestPending || s == RequestApproved || s == RequestDownloading
}

// RequestSeasons says which seasons of a series are requested.
type RequestSeasons string

// Seasons a request may ask for.
const (
	SeasonsAll    RequestSeasons = "all"
	SeasonsFirst  RequestSeasons = "first"
	SeasonsLatest RequestSeasons = "latest"
	// SeasonsChosen asks for the seasons of MediaRequest.SeasonNumbers.
	SeasonsChosen RequestSeasons = "chosen"
)

// SeriesType is how Sonarr numbers and searches the episodes of a series.
type SeriesType string

// Series types of Sonarr.
const (
	SeriesStandard SeriesType = "standard"
	SeriesAnime    SeriesType = "anime"
	SeriesDaily    SeriesType = "daily"
)

// DefaultRequestQuota is the number of requests an account may make in RequestQuotaWindow, unless
// an administrator sets another (0: no limit).
const DefaultRequestQuota = 10

// RequestQuotaWindow is the period the quota counts over.
const RequestQuotaWindow = 7 * 24 * time.Hour

// RequestDestination is where requests of a kind land: a library and, on the instance, a root
// folder and a quality profile.
type RequestDestination struct {
	ID   ID
	Name string
	// Kind is a family: series, movie, music or book.
	Kind        RequestKind
	LibraryID   ID
	LibraryName string
	RootFolder  string
	// QualityProfileName is the profile's name when it was chosen, for display.
	QualityProfileID   int
	QualityProfileName string
	SeriesType         SeriesType
	// MetadataProfileID is Lidarr's metadata profile (music only); its name is kept for display.
	MetadataProfileID   int
	MetadataProfileName string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// MediaRequest is a request for a title.
type MediaRequest struct {
	ID   ID
	Kind RequestKind
	// ExternalID is the TVDB ID of a series, the TMDB ID of a movie; 0 for the others.
	ExternalID int64
	// ExternalKey is the MusicBrainz ID of an artist or a release group, the OpenLibrary work ID
	// of a book; empty for series and movies.
	ExternalKey string
	Title       string
	// Subtitle is the artist of an album, the author of a book; empty otherwise.
	Subtitle string
	Year     int
	// Poster is the poster's address on TVDB, TMDB, MusicBrainz or OpenLibrary; the server serves
	// it to devices.
	Poster        string
	Status        RequestStatus
	Seasons       RequestSeasons
	SeasonNumbers []int
	// Destination is nil once its destination was deleted.
	Destination *RequestDestination
	AccountID   ID
	Username    string
	ProfileID   ID
	ProfileName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DecidedAt   *time.Time
	// DecidedBy is the name of the administrator who approved or declined it.
	DecidedBy     string
	DeclineReason string
	// Error says why it failed (RequestFailed).
	Error *Text
	// ArrID is the series, movie, artist or album on the instance once handed over; 0 before (and
	// for books).
	ArrID    int
	Progress float64
	// ItemID is the catalog item once available.
	ItemID            *ID
	EpisodesAvailable int
	EpisodesWanted    int
	AvailableAt       *time.Time
}

// Family is what a search or a destination is for: music for an artist or an album, the kind
// itself otherwise.
func (k RequestKind) Family() RequestKind {
	if k == RequestArtist || k == RequestAlbum {
		return RequestMusic
	}
	return k
}

// ExternalProvider is the provider of a kind's external IDs, as the catalog names them (NFO files,
// tags); empty for books, found by ISBN or by title and author.
func (k RequestKind) ExternalProvider() string {
	switch k {
	case RequestSeries:
		return "tvdb"
	case RequestMovie:
		return "tmdb"
	case RequestArtist:
		return "musicbrainz_artist"
	case RequestAlbum:
		return "musicbrainz_releasegroup"
	case RequestMusic, RequestBook:
	}
	return ""
}

// LibraryKind is the kind of library a request of that kind (or family) lands in.
func (k RequestKind) LibraryKind() LibraryKind {
	switch k.Family() {
	case RequestSeries:
		return LibraryShows
	case RequestMusic:
		return LibraryMusic
	case RequestBook:
		return LibraryBooks
	case RequestMovie, RequestArtist, RequestAlbum:
	}
	return LibraryMovies
}

// RequestableState says what a search result is to a profile.
type RequestableState string

const (
	// Requestable means it can be requested.
	Requestable RequestableState = "requestable"
	// RequestableAvailable means a library the profile sees has it.
	RequestableAvailable RequestableState = "available"
	// RequestableRequested means a request for it is open.
	RequestableRequested RequestableState = "requested"
	// RequestableTracked means the instance already has it, or a library the profile does not see.
	RequestableTracked RequestableState = "tracked"
)

// RequestableTitle is a search result.
type RequestableTitle struct {
	Kind        RequestKind
	ExternalID  int64
	ExternalKey string
	Title       string
	Year        int
	Overview    string
	// Poster is the poster's address on TVDB, TMDB, MusicBrainz or OpenLibrary.
	Poster string
	// Network of a series, studio of a movie, artist of an album, author of a book.
	Network     string
	SeasonCount int
	State       RequestableState
	// ItemID is set when available, RequestID when requested.
	ItemID    *ID
	RequestID *ID
}

// RequestOptions is what a destination can use on the instance.
type RequestOptions struct {
	RootFolders     []RootFolder
	QualityProfiles []QualityProfile
	// MetadataProfiles are Lidarr's (music only).
	MetadataProfiles []QualityProfile
}

// RootFolder is a root folder of Sonarr or Radarr; FreeSpace is in bytes, 0 if unknown.
type RootFolder struct {
	Path      string
	FreeSpace int64
}

// QualityProfile is a quality profile of Sonarr, Radarr or Lidarr (or a metadata profile of
// Lidarr).
type QualityProfile struct {
	ID   int
	Name string
}

// Key is the request's external ID as text: its key, or the number of a series or a movie.
func (r MediaRequest) Key() string { return externalKey(r.ExternalID, r.ExternalKey) }

// Key is the title's external ID as text: its key, or the number of a series or a movie.
func (t RequestableTitle) Key() string { return externalKey(t.ExternalID, t.ExternalKey) }

func externalKey(id int64, key string) string {
	if key != "" {
		return key
	}
	return strconv.FormatInt(id, 10)
}
