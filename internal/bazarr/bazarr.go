// Package bazarr talks to Bazarr, which finds subtitles for the series of Sonarr and the movies of
// Radarr (docs/design/subtitles.md): it lists what Bazarr knows, asks it to look for a subtitle and
// reads whether one arrived. It knows nothing about the database or the catalog.
package bazarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxResponse = 64 << 20

// Client calls the API of a Bazarr instance.
type Client struct {
	base string
	key  string
	http *http.Client
}

// New creates a client for the instance at base ("http://localhost:6767").
func New(base, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimSuffix(base, "/"), key: apiKey, http: hc}
}

// Error is a refusal from Bazarr, with its explanation.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// ErrUnauthorized is returned when the API key is rejected.
var ErrUnauthorized = errors.New("API key rejected")

func (c *Client) do(ctx context.Context, method, path string, q url.Values, out any) error {
	u := c.base + "/api" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", c.key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode/100 != 2:
		return &Error{Status: resp.StatusCode, Message: problem(data)}
	case out == nil:
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: unreadable response: %w", method, path, err)
	}
	return nil
}

// problem pulls the explanation out of a refusal: a JSON string or a message, never a page of HTML.
func problem(data []byte) string {
	var s string
	if json.Unmarshal(data, &s) == nil {
		return strings.TrimSpace(s)
	}
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &m) == nil && m.Message != "" {
		return m.Message
	}
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "<") || len(text) > 200 {
		return ""
	}
	return text
}

// Version returns the version of Bazarr, which proves the address and the key.
func (c *Client) Version(ctx context.Context) (string, error) {
	var st struct {
		Data struct {
			Version string `json:"bazarr_version"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/system/status", nil, &st); err != nil {
		return "", err
	}
	if st.Data.Version == "" {
		return "", &Error{Status: http.StatusOK, Message: "not a Bazarr status"}
	}
	return st.Data.Version, nil
}

// Language is a language Bazarr can look subtitles up in.
type Language struct {
	// Code is its two-letter code ("fr"), the one searches take.
	Code string
	Name string
}

// Languages lists the languages enabled in Bazarr.
func (c *Client) Languages(ctx context.Context) ([]Language, error) {
	var list []struct {
		Name    string `json:"name"`
		Code2   string `json:"code2"`
		Enabled bool   `json:"enabled"`
	}
	if err := c.do(ctx, http.MethodGet, "/system/languages", nil, &list); err != nil {
		return nil, err
	}
	var out []Language
	for _, l := range list {
		if l.Enabled && l.Code2 != "" {
			out = append(out, Language{Code: l.Code2, Name: l.Name})
		}
	}
	return out, nil
}

// Subtitle is a subtitle Bazarr knows for a video.
type Subtitle struct {
	// Language is its two-letter code.
	Language        string
	Forced          bool
	HearingImpaired bool
	// Path is its file as Bazarr sees it; empty for a track inside the video.
	Path string
}

type subtitleJSON struct {
	Code2  string `json:"code2"`
	Path   string `json:"path"`
	Forced bool   `json:"forced"`
	HI     bool   `json:"hi"`
}

func subtitles(list []subtitleJSON) []Subtitle {
	out := make([]Subtitle, 0, len(list))
	for _, s := range list {
		out = append(out, Subtitle{Language: s.Code2, Forced: s.Forced, HearingImpaired: s.HI, Path: s.Path})
	}
	return out
}

// Series is a series Bazarr follows.
type Series struct {
	// ID is the series on Sonarr, which is how Bazarr names it.
	ID     int
	TvdbID int64
	Title  string
	// Path is its folder as Bazarr sees it.
	Path string
}

// Series lists the series Bazarr follows.
func (c *Client) Series(ctx context.Context) ([]Series, error) {
	var page struct {
		Data []struct {
			ID     int    `json:"sonarrSeriesId"`
			TvdbID int64  `json:"tvdbId"`
			Title  string `json:"title"`
			Path   string `json:"path"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/series", url.Values{"start": {"0"}, "length": {"-1"}}, &page); err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(page.Data))
	for _, s := range page.Data {
		out = append(out, Series{ID: s.ID, TvdbID: s.TvdbID, Title: s.Title, Path: s.Path})
	}
	return out, nil
}

// Video is an episode or a movie Bazarr follows, with the subtitles it knows for it.
type Video struct {
	// ID is the episode on Sonarr or the movie on Radarr; SeriesID is the series of an episode.
	ID       int
	SeriesID int
	// Season and Episode number an episode.
	Season, Episode int
	Title           string
	// ImdbID is the IMDb ID of a movie ("tt0113277"), when Bazarr has it.
	ImdbID string
	// Path is its file as Bazarr sees it.
	Path      string
	Subtitles []Subtitle
}

// Episodes lists the episodes of a series.
func (c *Client) Episodes(ctx context.Context, seriesID int) ([]Video, error) {
	return c.episodes(ctx, url.Values{"seriesid[]": {strconv.Itoa(seriesID)}})
}

// Episode reads one episode; ok is false if Bazarr does not have it.
func (c *Client) Episode(ctx context.Context, episodeID int) (v Video, ok bool, err error) {
	list, err := c.episodes(ctx, url.Values{"episodeid[]": {strconv.Itoa(episodeID)}})
	if err != nil || len(list) == 0 {
		return Video{}, false, err
	}
	return list[0], true, nil
}

func (c *Client) episodes(ctx context.Context, q url.Values) ([]Video, error) {
	var page struct {
		Data []struct {
			ID        int            `json:"sonarrEpisodeId"`
			SeriesID  int            `json:"sonarrSeriesId"`
			Season    int            `json:"season"`
			Episode   int            `json:"episode"`
			Title     string         `json:"title"`
			Path      string         `json:"path"`
			Subtitles []subtitleJSON `json:"subtitles"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/episodes", q, &page); err != nil {
		return nil, err
	}
	out := make([]Video, 0, len(page.Data))
	for _, e := range page.Data {
		out = append(out, Video{
			ID: e.ID, SeriesID: e.SeriesID, Season: e.Season, Episode: e.Episode, Title: e.Title, Path: e.Path,
			Subtitles: subtitles(e.Subtitles),
		})
	}
	return out, nil
}

// Movies lists the movies Bazarr follows.
func (c *Client) Movies(ctx context.Context) ([]Video, error) {
	return c.movies(ctx, url.Values{"start": {"0"}, "length": {"-1"}})
}

// Movie reads one movie; ok is false if Bazarr does not have it.
func (c *Client) Movie(ctx context.Context, movieID int) (v Video, ok bool, err error) {
	list, err := c.movies(ctx, url.Values{"radarrid[]": {strconv.Itoa(movieID)}})
	if err != nil || len(list) == 0 {
		return Video{}, false, err
	}
	return list[0], true, nil
}

func (c *Client) movies(ctx context.Context, q url.Values) ([]Video, error) {
	var page struct {
		Data []struct {
			ID        int            `json:"radarrId"`
			ImdbID    string         `json:"imdbId"`
			Title     string         `json:"title"`
			Path      string         `json:"path"`
			Subtitles []subtitleJSON `json:"subtitles"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/movies", q, &page); err != nil {
		return nil, err
	}
	out := make([]Video, 0, len(page.Data))
	for _, m := range page.Data {
		out = append(out, Video{ID: m.ID, ImdbID: m.ImdbID, Title: m.Title, Path: m.Path, Subtitles: subtitles(m.Subtitles)})
	}
	return out, nil
}

// Wanted describes the subtitle to look for.
type Wanted struct {
	// Language is a two-letter code Bazarr has enabled.
	Language        string
	Forced          bool
	HearingImpaired bool
}

func (w Wanted) query() url.Values {
	return url.Values{
		"language": {w.Language}, "forced": {strconv.FormatBool(w.Forced)}, "hi": {strconv.FormatBool(w.HearingImpaired)},
	}
}

// SearchEpisode asks Bazarr to look for a subtitle of an episode and to save the best one next to
// the video. Recent versions answer at once and search in the background, older ones answer when
// they are done: either way, what was found is read afterwards (Episode).
func (c *Client) SearchEpisode(ctx context.Context, seriesID, episodeID int, w Wanted) error {
	q := w.query()
	q.Set("seriesid", strconv.Itoa(seriesID))
	q.Set("episodeid", strconv.Itoa(episodeID))
	return c.do(ctx, http.MethodPatch, "/episodes/subtitles", q, nil)
}

// SearchMovie is SearchEpisode for a movie.
func (c *Client) SearchMovie(ctx context.Context, movieID int, w Wanted) error {
	q := w.query()
	q.Set("radarrid", strconv.Itoa(movieID))
	return c.do(ctx, http.MethodPatch, "/movies/subtitles", q, nil)
}
