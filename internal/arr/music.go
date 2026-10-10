package arr

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// Music: searching Lidarr for artists and albums, adding them, following them (requests,
// docs/design/requests.md). Lidarr identifies an artist by its MusicBrainz ID and an album by the
// MusicBrainz ID of its release group.

// MusicTitle is an artist or an album, found by a search or tracked by Lidarr.
type MusicTitle struct {
	// Album is false for an artist.
	Album bool
	// MBID is the MusicBrainz ID of the artist, or of the album's release group.
	MBID string
	// Title is the artist's name or the album's title.
	Title string
	// Artist and ArtistMBID are the album's artist.
	Artist     string
	ArtistMBID string
	// Year of an album, 0 if unknown.
	Year     int
	Overview string
	// Poster is the album's cover or the artist's poster; empty if none.
	Poster string
	// Type is the album's type ("Album", "EP", "Single"...) or the artist's ("Group", "Person").
	Type string
	// ArrID is the artist or album on Lidarr; 0 if Lidarr does not have it.
	ArrID     int
	ArtistID  int
	Monitored bool
	// TrackFiles counts the tracks with a file, Tracks the monitored ones.
	TrackFiles int
	Tracks     int
}

type musicImage struct {
	CoverType string `json:"coverType"`
	RemoteURL string `json:"remoteUrl"`
}

type musicStats struct {
	TrackFileCount int `json:"trackFileCount"`
	TrackCount     int `json:"trackCount"`
}

type artistJSON struct {
	ID              int          `json:"id"`
	ForeignArtistID string       `json:"foreignArtistId"`
	ArtistName      string       `json:"artistName"`
	Overview        string       `json:"overview"`
	ArtistType      string       `json:"artistType"`
	Monitored       bool         `json:"monitored"`
	Images          []musicImage `json:"images"`
	Statistics      musicStats   `json:"statistics"`
}

type albumJSON struct {
	ID             int          `json:"id"`
	ArtistID       int          `json:"artistId"`
	ForeignAlbumID string       `json:"foreignAlbumId"`
	Title          string       `json:"title"`
	Overview       string       `json:"overview"`
	AlbumType      string       `json:"albumType"`
	ReleaseDate    string       `json:"releaseDate"`
	Monitored      bool         `json:"monitored"`
	Images         []musicImage `json:"images"`
	RemoteCover    string       `json:"remoteCover"`
	Artist         *artistJSON  `json:"artist"`
	Statistics     musicStats   `json:"statistics"`
}

func image(images []musicImage, kind string) string {
	for _, i := range images {
		if i.CoverType == kind && i.RemoteURL != "" {
			return i.RemoteURL
		}
	}
	return ""
}

func (a artistJSON) title() MusicTitle {
	return MusicTitle{
		MBID: a.ForeignArtistID, Title: a.ArtistName, Overview: a.Overview, Type: a.ArtistType,
		Poster: cmp.Or(image(a.Images, "poster"), image(a.Images, "fanart")), ArrID: a.ID, ArtistID: a.ID,
		Monitored: a.Monitored, TrackFiles: a.Statistics.TrackFileCount, Tracks: a.Statistics.TrackCount,
	}
}

func (a albumJSON) title() MusicTitle {
	t := MusicTitle{
		Album: true, MBID: a.ForeignAlbumID, Title: a.Title, Overview: a.Overview, Type: a.AlbumType,
		Poster: cmp.Or(image(a.Images, "cover"), a.RemoteCover), ArrID: a.ID, ArtistID: a.ArtistID,
		Monitored: a.Monitored, TrackFiles: a.Statistics.TrackFileCount, Tracks: a.Statistics.TrackCount,
	}
	if len(a.ReleaseDate) >= 4 {
		t.Year, _ = strconv.Atoi(a.ReleaseDate[:4])
	}
	if a.Artist != nil {
		t.Artist, t.ArtistMBID = a.Artist.ArtistName, a.Artist.ForeignArtistID
	}
	return t
}

// SearchMusic looks artists and albums up, best match first.
func (c *Client) SearchMusic(ctx context.Context, term string) ([]MusicTitle, error) {
	var list []struct {
		Artist *artistJSON `json:"artist"`
		Album  *albumJSON  `json:"album"`
	}
	if err := c.do(ctx, http.MethodGet, "/search?term="+url.QueryEscape(term), nil, &list); err != nil {
		return nil, err
	}
	out := make([]MusicTitle, 0, len(list))
	for _, r := range list {
		switch {
		case r.Album != nil:
			out = append(out, r.Album.title())
		case r.Artist != nil:
			out = append(out, r.Artist.title())
		}
	}
	return out, nil
}

// FindMusic looks an artist or an album up by its MusicBrainz ID; false if there is none.
func (c *Client) FindMusic(ctx context.Context, album bool, mbid string) (MusicTitle, bool, error) {
	r, err := c.lookupMusic(ctx, album, mbid)
	if err != nil || r == nil {
		return MusicTitle{}, false, err
	}
	return r.title(album), true, nil
}

// lookupMusic finds an artist or an album by its MusicBrainz ID, as a search returns it (the body
// adding it needs); nil if there is none.
func (c *Client) lookupMusic(ctx context.Context, album bool, mbid string) (resource, error) {
	path, field := "/artist/lookup", "foreignArtistId"
	if album {
		path, field = "/album/lookup", "foreignAlbumId"
	}
	var list []resource
	if err := c.do(ctx, http.MethodGet, path+"?term="+url.QueryEscape("lidarr:"+mbid), nil, &list); err != nil {
		return nil, err
	}
	for _, r := range list {
		if r[field] == mbid {
			return r, nil
		}
	}
	return nil, nil
}

// title converts a lookup resource.
func (r resource) title(album bool) MusicTitle {
	if album {
		var a albumJSON
		convert(r, &a)
		return a.title()
	}
	var a artistJSON
	convert(r, &a)
	return a.title()
}

// TrackedMusic returns the artist or album Lidarr has with that MusicBrainz ID; false if it has
// none.
func (c *Client) TrackedMusic(ctx context.Context, album bool, mbid string) (MusicTitle, bool, error) {
	if album {
		var list []albumJSON
		if err := c.do(ctx, http.MethodGet, "/album?foreignAlbumId="+url.QueryEscape(mbid), nil, &list); err != nil {
			return MusicTitle{}, false, err
		}
		for _, a := range list {
			if a.ForeignAlbumID == mbid && a.ID > 0 {
				return a.title(), true, nil
			}
		}
		return MusicTitle{}, false, nil
	}
	var list []artistJSON
	if err := c.do(ctx, http.MethodGet, "/artist?mbId="+url.QueryEscape(mbid), nil, &list); err != nil {
		return MusicTitle{}, false, err
	}
	for _, a := range list {
		if a.ForeignArtistID == mbid && a.ID > 0 {
			return a.title(), true, nil
		}
	}
	return MusicTitle{}, false, nil
}

// GetMusic reads an artist or an album of Lidarr by its ID there.
func (c *Client) GetMusic(ctx context.Context, album bool, arrID int) (MusicTitle, error) {
	if album {
		var a albumJSON
		err := c.do(ctx, http.MethodGet, "/album/"+strconv.Itoa(arrID), nil, &a)
		return a.title(), err
	}
	var a artistJSON
	err := c.do(ctx, http.MethodGet, "/artist/"+strconv.Itoa(arrID), nil, &a)
	return a.title(), err
}

// MetadataProfiles lists Lidarr's metadata profiles (which kinds of releases an artist's albums
// include).
func (c *Client) MetadataProfiles(ctx context.Context) ([]QualityProfile, error) {
	var list []QualityProfile
	err := c.do(ctx, http.MethodGet, "/metadataprofile", nil, &list)
	return list, err
}

// MusicOptions says where and how an artist or an album is added.
type MusicOptions struct {
	RootFolder        string
	QualityProfileID  int
	MetadataProfileID int
	// Albums of an artist: "all" (and the next ones as they come out), "first" or "latest".
	Albums string
}

// artistMonitor is Lidarr's addOptions.monitor for the albums asked for.
func artistMonitor(albums string) string {
	switch albums {
	case "first", "latest":
		return albums
	}
	return "all"
}

// AddArtist adds an artist to Lidarr, monitored with the albums asked for, and searches for them.
// An artist Lidarr already has is monitored and searched instead. It returns the artist's ID on
// Lidarr.
func (c *Client) AddArtist(ctx context.Context, mbid string, o MusicOptions) (int, error) {
	if t, ok, err := c.TrackedMusic(ctx, false, mbid); err != nil {
		return 0, err
	} else if ok {
		return t.ArrID, c.monitorArtist(ctx, t.ArrID, o)
	}
	r, err := c.lookupMusic(ctx, false, mbid)
	if err != nil {
		return 0, err
	}
	if r == nil {
		return 0, &Error{Status: http.StatusNotFound, Message: "unknown artist " + mbid}
	}
	r["qualityProfileId"] = o.QualityProfileID
	r["metadataProfileId"] = o.MetadataProfileID
	r["rootFolderPath"] = o.RootFolder
	r["monitored"] = true
	r["monitorNewItems"] = "none"
	if o.Albums == "all" || o.Albums == "" {
		r["monitorNewItems"] = "all"
	}
	r["addOptions"] = map[string]any{"monitor": artistMonitor(o.Albums), "searchForMissingAlbums": true}
	var added struct {
		ID int `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/artist", r, &added); err != nil {
		return 0, err
	}
	return added.ID, nil
}

// monitorArtist monitors an artist Lidarr has, with the albums asked for, then searches them.
func (c *Client) monitorArtist(ctx context.Context, arrID int, o MusicOptions) error {
	var r resource
	if err := c.do(ctx, http.MethodGet, "/artist/"+strconv.Itoa(arrID), nil, &r); err != nil {
		return err
	}
	if r == nil {
		return &Error{Status: http.StatusNotFound, Message: "unknown artist " + strconv.Itoa(arrID)}
	}
	r["monitored"] = true
	if o.Albums == "all" || o.Albums == "" {
		r["monitorNewItems"] = "all"
	}
	if err := c.do(ctx, http.MethodPut, "/artist/"+strconv.Itoa(arrID), r, nil); err != nil {
		return err
	}
	var albums []albumJSON
	if err := c.do(ctx, http.MethodGet, "/album?artistId="+strconv.Itoa(arrID), nil, &albums); err != nil {
		return err
	}
	ids := albumsWanted(albums, o.Albums)
	if len(ids) == 0 {
		return nil
	}
	return c.searchAlbums(ctx, ids)
}

// albumsWanted lists the albums an artist request asks for: all, or the first or latest album
// (by release date; "Album" types first, any type if the artist has no album).
func albumsWanted(albums []albumJSON, which string) []int {
	if len(albums) == 0 {
		return nil
	}
	if which != "first" && which != "latest" {
		ids := make([]int, len(albums))
		for i, a := range albums {
			ids[i] = a.ID
		}
		return ids
	}
	pool := slices.DeleteFunc(slices.Clone(albums), func(a albumJSON) bool { return a.AlbumType != "Album" })
	if len(pool) == 0 {
		pool = albums
	}
	slices.SortFunc(pool, func(x, y albumJSON) int { return cmp.Compare(x.ReleaseDate, y.ReleaseDate) })
	if which == "first" {
		return []int{pool[0].ID}
	}
	return []int{pool[len(pool)-1].ID}
}

// searchAlbums monitors albums and searches for them.
func (c *Client) searchAlbums(ctx context.Context, ids []int) error {
	if err := c.do(ctx, http.MethodPut, "/album/monitor", map[string]any{"albumIds": ids, "monitored": true}, nil); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/command", map[string]any{"name": "AlbumSearch", "albumIds": ids}, nil)
}

// AddAlbum adds an album to Lidarr, monitored, and searches for it. Its artist is added if Lidarr
// does not have it, monitored but with none of its other albums. An album Lidarr already has is
// monitored and searched instead. It returns the album's ID on Lidarr and its artist's.
func (c *Client) AddAlbum(ctx context.Context, mbid string, o MusicOptions) (albumID, artistID int, err error) {
	if t, ok, err := c.TrackedMusic(ctx, true, mbid); err != nil {
		return 0, 0, err
	} else if ok {
		if err := c.monitorArtistOnly(ctx, t.ArtistID); err != nil {
			return 0, 0, err
		}
		return t.ArrID, t.ArtistID, c.searchAlbums(ctx, []int{t.ArrID})
	}
	r, err := c.lookupMusic(ctx, true, mbid)
	if err != nil {
		return 0, 0, err
	}
	if r == nil {
		return 0, 0, &Error{Status: http.StatusNotFound, Message: "unknown album " + mbid}
	}
	if artist, ok := r["artist"].(map[string]any); ok {
		artist["qualityProfileId"] = o.QualityProfileID
		artist["metadataProfileId"] = o.MetadataProfileID
		artist["rootFolderPath"] = o.RootFolder
		artist["monitored"] = true
		artist["monitorNewItems"] = "none"
		artist["addOptions"] = map[string]any{"monitor": "none", "searchForMissingAlbums": false}
	}
	r["monitored"] = true
	r["addOptions"] = map[string]any{"searchForNewAlbum": true}
	var added albumJSON
	if err := c.do(ctx, http.MethodPost, "/album", r, &added); err != nil {
		return 0, 0, err
	}
	return added.ID, added.ArtistID, nil
}

// monitorArtistOnly makes sure an artist is monitored (Lidarr grabs nothing for an unmonitored
// artist), without touching its albums.
func (c *Client) monitorArtistOnly(ctx context.Context, arrID int) error {
	if arrID == 0 {
		return nil
	}
	var r resource
	if err := c.do(ctx, http.MethodGet, "/artist/"+strconv.Itoa(arrID), nil, &r); err != nil {
		return err
	}
	if r == nil || r["monitored"] == true {
		return nil
	}
	r["monitored"] = true
	return c.do(ctx, http.MethodPut, "/artist/"+strconv.Itoa(arrID), r, nil)
}

// convert reads a configuration resource as a typed value.
func convert(r resource, out any) {
	if b, err := json.Marshal(r); err == nil {
		_ = json.Unmarshal(b, out)
	}
}
