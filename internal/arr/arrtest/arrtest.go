// Package arrtest fakes Sonarr or Radarr (API v3), or Lidarr (API v1), for tests: identity, Kodi
// metadata, notifications (the webhook is tried when saved, as the real ones do), refresh commands,
// tracked series, movies or artists, and what requests use.
package arrtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/laterna-project/laterna/internal/arr"
)

// Key is the API key the fake accepts.
const Key = "test-key"

// Server is a fake Sonarr, Radarr or Lidarr.
type Server struct {
	*httptest.Server
	kind arr.Kind

	mu sync.Mutex
	// Kodi metadata enabled or not, and the value of each option.
	KodiEnabled bool
	KodiOptions map[string]bool
	// Folders are the tracked series or movies (paths as the instance sees them).
	Folders []arr.Folder
	// Hook is Laterna's saved webhook, nil if none.
	Hook *Hook
	// Refreshes counts the refreshes asked for. A command finishes the second time it is read.
	Refreshes int
	reads     map[int]int
	// Commands lists the names of the commands received, in order.
	Commands []string
	// Catalog is what a search finds (TVDB or TMDB as the instance sees it).
	Catalog []Entry
	// Titles are the series or movies the instance has, by their ID there.
	Titles map[int]*Title
	// Roots and Profiles are the root folders and quality profiles of the instance.
	Roots    []arr.RootFolder
	Profiles []arr.QualityProfile
	// Queue is what the instance is downloading.
	Queue []arr.Download
	// Lidarr: what a search finds, the artists and albums it has, its metadata profiles.
	Music            []MusicEntry
	Artists          map[int]*Artist
	Albums           map[int]*Album
	MetadataProfiles []arr.QualityProfile
}

// Entry is a title a search can find.
type Entry struct {
	ExternalID int64
	Title      string
	Year       int
	Seasons    []int
	Poster     string
}

// Title is a series or a movie the instance has.
type Title struct {
	Entry
	ID               int
	Monitored        bool
	MonitoredSeasons []int
	RootFolder       string
	QualityProfileID int
	SeriesType       string
	// Monitor is the addOptions.monitor it was added with.
	Monitor        string
	EpisodeFiles   int
	EpisodesWanted int
	HasFile        bool
}

// Hook is a saved webhook.
type Hook struct {
	ID                 int
	URL, User, Secret  string
	OnDownload, Rename bool
}

// New starts a fake Sonarr, Radarr or Lidarr that is stopped when the test ends.
func New(t *testing.T, kind arr.Kind) *Server {
	t.Helper()
	s := &Server{
		kind: kind, KodiOptions: map[string]bool{}, reads: map[int]int{}, Titles: map[int]*Title{},
		Roots:            []arr.RootFolder{{Path: "/data/media/" + map[arr.Kind]string{arr.Sonarr: "shows", arr.Radarr: "movies", arr.Lidarr: "music"}[kind], FreeSpace: 1 << 40}},
		Profiles:         []arr.QualityProfile{{ID: 1, Name: "Any"}, {ID: 7, Name: "HD Bluray + WEB"}},
		Artists:          map[int]*Artist{},
		Albums:           map[int]*Album{},
		MetadataProfiles: []arr.QualityProfile{{ID: 1, Name: "Standard"}, {ID: 2, Name: "None"}},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

var kodiFields = map[arr.Kind][]string{
	arr.Sonarr: {"seriesMetadata", "seriesMetadataEpisodeGuide", "episodeMetadata", "episodeImageThumb", "seriesImages", "seasonImages", "episodeImages"},
	arr.Radarr: {"movieMetadata", "useMovieNfo", "movieMetadataLanguage", "movieImages"},
	arr.Lidarr: {"artistMetadata", "albumMetadata", "artistImages", "albumImages"},
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Api-Key") != Key {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v3"), "/api/v1")
	if (s.kind == arr.Lidarr) != strings.HasPrefix(r.URL.Path, "/api/v1") {
		http.NotFound(w, r) // the wrong version of the API
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	if s.kind == arr.Lidarr && s.serveMusic(w, r, path, body, write) {
		return
	}
	if s.serveTitles(w, r, path, body, write) {
		return
	}
	switch {
	case path == "/system/status":
		write(map[string]any{"appName": s.kind.Name(), "version": "9.9.9"})
	case path == "/metadata" && r.Method == http.MethodGet:
		write([]any{map[string]any{"id": 7, "name": "Roksbox", "implementation": "RoksboxMetadata", "enable": false, "fields": []any{}}, s.kodi()})
	case path == "/metadata/1" && r.Method == http.MethodPut:
		s.KodiEnabled = body["enable"] == true
		for _, f := range fields(body) {
			if v, ok := f["value"].(bool); ok {
				s.KodiOptions[f["name"].(string)] = v
			}
		}
		write(body)
	case path == "/notification" && r.Method == http.MethodGet:
		list := []any{map[string]any{"id": 1, "name": "Discord", "implementation": "Discord", "fields": []any{}}}
		if s.Hook != nil {
			list = append(list, s.hookResource())
		}
		write(list)
	case path == "/notification/schema":
		write([]any{map[string]any{
			"implementation": "Webhook", "name": "", "presets": []any{}, "supportsOnDownload": true, "supportsOnRename": true,
			"supportsOnUpgrade": true, "supportsOnReleaseImport": s.kind == arr.Lidarr, "onDownload": false, "onRename": false,
			"fields": []any{map[string]any{"name": "url"}, map[string]any{"name": "method", "value": 1}, map[string]any{"name": "username"}, map[string]any{"name": "password"}},
		}})
	case (path == "/notification" && r.Method == http.MethodPost) || (strings.HasPrefix(path, "/notification/") && r.Method == http.MethodPut):
		h := &Hook{ID: 2, OnDownload: body["onDownload"] == true || body["onReleaseImport"] == true, Rename: body["onRename"] == true}
		for _, f := range fields(body) {
			v, _ := f["value"].(string)
			switch f["name"] {
			case "url":
				h.URL = v
			case "username":
				h.User = v
			case "password":
				h.Secret = v
			}
		}
		// Like the real ones: saving tries the webhook.
		if err := callHook(h, map[string]any{"eventType": "Test"}); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			write([]any{map[string]any{"propertyName": "Url", "errorMessage": "Unable to send test message: " + err.Error()}})
			return
		}
		s.Hook = h
		write(body)
	case strings.HasPrefix(path, "/notification/") && r.Method == http.MethodDelete:
		s.Hook = nil
	case path == "/command" && r.Method == http.MethodPost:
		name, _ := body["name"].(string)
		s.Commands = append(s.Commands, name)
		s.Refreshes++
		write(map[string]any{"id": 100 + s.Refreshes, "name": body["name"], "status": "queued"})
	case strings.HasPrefix(path, "/command/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(path, "/command/"))
		s.reads[id]++
		status := "started"
		if s.reads[id] >= 2 {
			status = "completed"
		}
		write(map[string]any{"id": id, "status": status})
	case path == "/series" && s.kind == arr.Sonarr:
		var list []any
		for _, f := range s.Folders {
			n := 0
			if f.HasFiles {
				n = 1
			}
			list = append(list, map[string]any{"title": f.Title, "path": f.Path, "statistics": map[string]any{"episodeFileCount": n}})
		}
		write(list)
	case path == "/movie" && s.kind == arr.Radarr:
		var list []any
		for _, f := range s.Folders {
			m := map[string]any{"title": f.Title, "path": f.Path, "hasFile": f.HasFiles}
			if f.File != "" {
				m["movieFile"] = map[string]any{"relativePath": f.File}
			}
			list = append(list, m)
		}
		write(list)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) kodi() map[string]any {
	var fs []any
	for _, name := range kodiFields[s.kind] {
		fs = append(fs, map[string]any{"name": name, "label": "Label " + name, "value": s.KodiOptions[name]})
	}
	return map[string]any{"id": 1, "name": "Kodi (XBMC) / Emby", "implementation": "XbmcMetadata", "enable": s.KodiEnabled, "fields": fs}
}

func (s *Server) hookResource() map[string]any {
	h := s.Hook
	return map[string]any{
		"id": h.ID, "name": arr.WebhookName, "implementation": "Webhook", "onDownload": h.OnDownload, "onRename": h.Rename,
		"onReleaseImport":    h.OnDownload,
		"supportsOnDownload": true, "supportsOnRename": true,
		"fields": []any{
			map[string]any{"name": "url", "value": h.URL},
			map[string]any{"name": "method", "value": 1},
			map[string]any{"name": "username", "value": h.User},
			map[string]any{"name": "password", "value": h.Secret},
		},
	}
}

func fields(body map[string]any) []map[string]any {
	var out []map[string]any
	list, _ := body["fields"].([]any)
	for _, f := range list {
		if m, ok := f.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// Send sends an event to the saved webhook, as the instance would.
func (s *Server) Send(event map[string]any) error {
	s.mu.Lock()
	h := s.Hook
	s.mu.Unlock()
	if h == nil {
		return fmt.Errorf("no webhook")
	}
	return callHook(h, event)
}

func callHook(h *Hook, event map[string]any) error {
	b, _ := json.Marshal(event)
	req, err := http.NewRequest(http.MethodPost, h.URL, bytes.NewReader(b)) //nolint:noctx // test call
	if err != nil {
		return err
	}
	req.SetBasicAuth(h.User, h.Secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// Set changes the state under the lock.
func (s *Server) Set(f func(s *Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// Get reads the state under the lock.
func (s *Server) Get(f func(s *Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}
