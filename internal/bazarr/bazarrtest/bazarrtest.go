// Package bazarrtest fakes Bazarr for tests: its status, its languages, the series, episodes and
// movies it follows with their subtitles, and subtitle searches.
package bazarrtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/laterna-project/laterna/internal/bazarr"
)

// Key is the API key the fake accepts.
const Key = "test-key"

// Search is a subtitle search the fake received.
type Search struct {
	// SeriesID is 0 for a movie.
	SeriesID, VideoID int
	Wanted            bazarr.Wanted
}

// Server is a fake Bazarr.
type Server struct {
	*httptest.Server

	mu sync.Mutex
	// Languages are the enabled languages; Disabled those Bazarr knows but does not look up.
	Languages []bazarr.Language
	Disabled  []bazarr.Language
	Series    []bazarr.Series
	// Episodes and Movies are what Bazarr follows, with the subtitles it knows.
	Episodes []*bazarr.Video
	Movies   []*bazarr.Video
	// Find is what a search finds: the file of the subtitle it saves, or "" for none. Without it
	// nothing is ever found.
	Find func(v bazarr.Video, w bazarr.Wanted) string
	// Searches lists the searches received, in order.
	Searches []Search
}

// New starts a fake Bazarr that is stopped when the test ends.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{Languages: []bazarr.Language{{Code: "en", Name: "English"}, {Code: "fr", Name: "French"}}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Set changes the state of the fake under its lock.
func (s *Server) Set(f func(s *Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func subtitlesJSON(list []bazarr.Subtitle) []any {
	out := []any{}
	for _, sub := range list {
		m := map[string]any{"code2": sub.Language, "forced": sub.Forced, "hi": sub.HearingImpaired, "path": nil}
		if sub.Path != "" {
			m["path"] = sub.Path
		}
		out = append(out, m)
	}
	return out
}

func ids(r *http.Request, name string) []int {
	var out []int
	for _, v := range r.URL.Query()[name] {
		if n, err := strconv.Atoi(v); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-KEY") != Key {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/api/system/status":
		write(map[string]any{"data": map[string]any{"bazarr_version": "9.9.9", "sonarr_version": "4.0.0"}})
	case r.URL.Path == "/api/system/languages":
		list := []any{}
		for _, l := range s.Languages {
			list = append(list, map[string]any{"name": l.Name, "code2": l.Code, "code3": l.Code + "x", "enabled": true})
		}
		for _, l := range s.Disabled {
			list = append(list, map[string]any{"name": l.Name, "code2": l.Code, "code3": l.Code + "x", "enabled": false})
		}
		write(list)
	case r.URL.Path == "/api/series":
		list := []any{}
		for _, se := range s.Series {
			list = append(list, map[string]any{"sonarrSeriesId": se.ID, "tvdbId": se.TvdbID, "title": se.Title, "path": se.Path})
		}
		write(map[string]any{"data": list, "total": len(list)})
	case r.URL.Path == "/api/episodes" && r.Method == http.MethodGet:
		series, episodes := ids(r, "seriesid[]"), ids(r, "episodeid[]")
		if len(series) == 0 && len(episodes) == 0 {
			w.WriteHeader(http.StatusNotFound)
			write("Series or Episode ID not provided")
			return
		}
		list := []any{}
		for _, e := range s.Episodes {
			if (len(episodes) > 0 && slices.Contains(episodes, e.ID)) || (len(episodes) == 0 && slices.Contains(series, e.SeriesID)) {
				list = append(list, map[string]any{
					"sonarrEpisodeId": e.ID, "sonarrSeriesId": e.SeriesID, "season": e.Season, "episode": e.Episode,
					"title": e.Title, "path": e.Path, "subtitles": subtitlesJSON(e.Subtitles),
				})
			}
		}
		write(map[string]any{"data": list})
	case r.URL.Path == "/api/movies" && r.Method == http.MethodGet:
		wanted := ids(r, "radarrid[]")
		list := []any{}
		for _, m := range s.Movies {
			if len(wanted) == 0 || slices.Contains(wanted, m.ID) {
				list = append(list, map[string]any{
					"radarrId": m.ID, "imdbId": m.ImdbID, "title": m.Title, "path": m.Path, "subtitles": subtitlesJSON(m.Subtitles),
				})
			}
		}
		write(map[string]any{"data": list, "total": len(list)})
	case (r.URL.Path == "/api/episodes/subtitles" || r.URL.Path == "/api/movies/subtitles") && r.Method == http.MethodPatch:
		wanted := bazarr.Wanted{Language: q.Get("language"), Forced: q.Get("forced") == "true", HearingImpaired: q.Get("hi") == "true"}
		if wanted.Language == "" || q.Get("forced") == "" || q.Get("hi") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pool, id, series := s.Movies, q.Get("radarrid"), 0
		if r.URL.Path == "/api/episodes/subtitles" {
			pool, id = s.Episodes, q.Get("episodeid")
			series, _ = strconv.Atoi(q.Get("seriesid"))
		}
		n, _ := strconv.Atoi(id)
		s.Searches = append(s.Searches, Search{SeriesID: series, VideoID: n, Wanted: wanted})
		for _, v := range pool {
			if v.ID != n {
				continue
			}
			if s.Find != nil {
				if path := s.Find(*v, wanted); path != "" {
					v.Subtitles = append(v.Subtitles, bazarr.Subtitle{
						Language: wanted.Language, Forced: wanted.Forced, HearingImpaired: wanted.HearingImpaired, Path: path,
					})
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Like the real one, which answers before it looks the video up.
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}
