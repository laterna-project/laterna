package arr

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Release is something the instance expects on a given day and monitors: an episode (Sonarr), a
// movie (Radarr) or an album (Lidarr).
type Release struct {
	// Title of the episode, the movie or the album.
	Title string
	// Parent is the series of an episode or the artist of an album; empty for a movie.
	Parent string
	// ExternalID names what the catalog would know it by: the TVDB ID of the series, the TMDB ID of
	// the movie, the MusicBrainz ID of the artist.
	ExternalID string
	// Season and Episode number an episode.
	Season, Episode int
	// At is when the episode airs, or the day a movie comes out at home or an album is released.
	At time.Time
	// AllDay is true when At is a day and not a moment (movies, albums).
	AllDay bool
	// HasFile is true once the instance has it.
	HasFile bool
	// RootFolder is the root folder of the series, the movie or the artist on the instance.
	RootFolder string
	// Certification is the rating the instance knows ("TV-14", "PG-13"); empty for an album.
	Certification string
	// Poster is the address of the poster of the series or the movie, or of the album's cover.
	Poster string
}

type calendarImage = musicImage

// calendarEntry is what Calendar reads of an entry, whatever the instance.
type calendarEntry struct {
	Title   string `json:"title"`
	HasFile bool   `json:"hasFile"`
	// Sonarr.
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	AirDateUTC    string `json:"airDateUtc"`
	Series        *struct {
		Title          string          `json:"title"`
		TvdbID         int64           `json:"tvdbId"`
		Path           string          `json:"path"`
		RootFolderPath string          `json:"rootFolderPath"`
		Certification  string          `json:"certification"`
		Images         []calendarImage `json:"images"`
	} `json:"series"`
	// Radarr.
	TmdbID          int64           `json:"tmdbId"`
	DigitalRelease  string          `json:"digitalRelease"`
	PhysicalRelease string          `json:"physicalRelease"`
	Path            string          `json:"path"`
	RootFolderPath  string          `json:"rootFolderPath"`
	Certification   string          `json:"certification"`
	Images          []calendarImage `json:"images"`
	// Lidarr.
	ReleaseDate string `json:"releaseDate"`
	RemoteCover string `json:"remoteCover"`
	Artist      *struct {
		ArtistName      string `json:"artistName"`
		ForeignArtistID string `json:"foreignArtistId"`
		Path            string `json:"path"`
		RootFolderPath  string `json:"rootFolderPath"`
	} `json:"artist"`
	Statistics musicStats `json:"statistics"`
}

// Calendar lists what the instance monitors and expects between two moments, in no particular
// order. A movie counts on the day it comes out at home (digital, else physical): its day in
// theaters brings nothing to a library.
func (c *Client) Calendar(ctx context.Context, from, to time.Time) ([]Release, error) {
	q := url.Values{
		"start":       {from.UTC().Format(time.RFC3339)},
		"end":         {to.UTC().Format(time.RFC3339)},
		"unmonitored": {"false"},
	}
	switch c.kind {
	case Sonarr:
		q.Set("includeSeries", "true")
	case Lidarr:
		q.Set("includeArtist", "true")
	case Radarr:
	}
	var list []calendarEntry
	if err := c.do(ctx, http.MethodGet, "/calendar?"+q.Encode(), nil, &list); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(list))
	for _, e := range list {
		r, ok := c.release(e)
		if ok && !r.At.Before(from) && !r.At.After(to) {
			out = append(out, r)
		}
	}
	return out, nil
}

// release reads an entry of the calendar; ok is false when it lacks what makes it one (its series,
// its artist, a date at home).
func (c *Client) release(e calendarEntry) (Release, bool) {
	r := Release{Title: e.Title, HasFile: e.HasFile}
	switch c.kind {
	case Sonarr:
		if e.Series == nil || e.Series.TvdbID == 0 {
			return Release{}, false
		}
		at, err := time.Parse(time.RFC3339, e.AirDateUTC)
		if err != nil {
			return Release{}, false
		}
		r.Parent, r.ExternalID = e.Series.Title, strconv.FormatInt(e.Series.TvdbID, 10)
		r.Season, r.Episode, r.At = e.SeasonNumber, e.EpisodeNumber, at
		r.RootFolder = rootOf(e.Series.RootFolderPath, e.Series.Path)
		r.Certification, r.Poster = e.Series.Certification, image(e.Series.Images, "poster")
	case Radarr:
		at, ok := day(cmp.Or(e.DigitalRelease, e.PhysicalRelease))
		if !ok || e.TmdbID == 0 {
			return Release{}, false
		}
		r.ExternalID, r.At, r.AllDay = strconv.FormatInt(e.TmdbID, 10), at, true
		r.RootFolder = rootOf(e.RootFolderPath, e.Path)
		r.Certification, r.Poster = e.Certification, image(e.Images, "poster")
	case Lidarr:
		at, ok := day(e.ReleaseDate)
		if !ok || e.Artist == nil || e.Artist.ForeignArtistID == "" {
			return Release{}, false
		}
		r.Parent, r.ExternalID, r.At, r.AllDay = e.Artist.ArtistName, e.Artist.ForeignArtistID, at, true
		r.HasFile = e.Statistics.TrackCount > 0 && e.Statistics.TrackFileCount >= e.Statistics.TrackCount
		r.RootFolder = rootOf(e.Artist.RootFolderPath, e.Artist.Path)
		r.Poster = cmp.Or(image(e.Images, "cover"), e.RemoteCover)
	}
	return r, true
}

// day reads the day of a date the instance gives as a moment ("2026-03-14T00:00:00Z"): midnight UTC
// of that day.
func day(s string) (time.Time, bool) {
	if len(s) < len(time.DateOnly) {
		return time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, s[:len(time.DateOnly)])
	return t, err == nil
}

// rootOf is the root folder of a title: the one the instance names, else the folder above its own.
func rootOf(root, own string) string {
	clean := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/") }
	if root = clean(root); root != "" {
		return root
	}
	if own = clean(own); own != "" {
		return path.Dir(own)
	}
	return ""
}
