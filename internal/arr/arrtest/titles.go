package arrtest

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/laterna-project/laterna/internal/arr"
)

// serveTitles answers searches, the titles of the instance, root folders, quality profiles and the
// queue (requests). It returns false for anything else.
func (s *Server) serveTitles(w http.ResponseWriter, r *http.Request, path string, body map[string]any, write func(any)) bool {
	base, idField := "/series", "tvdbId"
	if s.kind == arr.Radarr {
		base, idField = "/movie", "tmdbId"
	}
	switch {
	case path == base+"/lookup":
		term := r.URL.Query().Get("term")
		var list []any
		for _, e := range s.Catalog {
			if strings.HasPrefix(term, "tvdb:") || strings.HasPrefix(term, "tmdb:") {
				if term[5:] != strconv.FormatInt(e.ExternalID, 10) {
					continue
				}
			} else if !strings.Contains(strings.ToLower(e.Title), strings.ToLower(term)) {
				continue
			}
			list = append(list, s.resource(e, s.byExternal(e.ExternalID)))
		}
		write(list)
	case path == base && r.Method == http.MethodGet && r.URL.Query().Get(idField) != "":
		id, _ := strconv.ParseInt(r.URL.Query().Get(idField), 10, 64)
		var list []any
		if t := s.byExternal(id); t != nil {
			list = append(list, s.resource(t.Entry, t))
		}
		write(list)
	case path == base && r.Method == http.MethodPost:
		profile, _ := body["qualityProfileId"].(float64)
		root, _ := body["rootFolderPath"].(string)
		if !slices.ContainsFunc(s.Profiles, func(p arr.QualityProfile) bool { return p.ID == int(profile) }) ||
			!slices.ContainsFunc(s.Roots, func(f arr.RootFolder) bool { return f.Path == root }) {
			w.WriteHeader(http.StatusBadRequest)
			write([]any{map[string]any{"propertyName": "RootFolderPath", "errorMessage": "Invalid root folder or quality profile"}})
			return true
		}
		ext, _ := body[idField].(float64)
		var entry Entry
		for _, e := range s.Catalog {
			if e.ExternalID == int64(ext) {
				entry = e
			}
		}
		t := &Title{Entry: entry, ID: len(s.Titles) + 1, Monitored: body["monitored"] == true, RootFolder: root, QualityProfileID: int(profile)}
		t.SeriesType, _ = body["seriesType"].(string)
		if opts, ok := body["addOptions"].(map[string]any); ok {
			t.Monitor, _ = opts["monitor"].(string)
		}
		switch t.Monitor {
		case "all":
			t.MonitoredSeasons = slices.Clone(entry.Seasons)
		case "firstSeason":
			t.MonitoredSeasons = entry.Seasons[:min(1, len(entry.Seasons))]
		case "latestSeason":
			t.MonitoredSeasons = entry.Seasons[max(0, len(entry.Seasons)-1):]
		case "none":
			t.Monitored = false // as Sonarr does
		}
		s.Titles[t.ID] = t
		write(s.resource(t.Entry, t))
	case strings.HasPrefix(path, base+"/"):
		id, err := strconv.Atoi(strings.TrimPrefix(path, base+"/"))
		t := s.Titles[id]
		if err != nil || t == nil {
			http.NotFound(w, r)
			return true
		}
		if r.Method == http.MethodPut {
			t.Monitored = body["monitored"] == true
			t.MonitoredSeasons = nil
			seasons, _ := body["seasons"].([]any)
			for _, x := range seasons {
				if m, ok := x.(map[string]any); ok && m["monitored"] == true {
					n, _ := m["seasonNumber"].(float64)
					t.MonitoredSeasons = append(t.MonitoredSeasons, int(n))
				}
			}
		}
		write(s.resource(t.Entry, t))
	case path == "/rootfolder":
		write(s.Roots)
	case path == "/qualityprofile":
		write(s.Profiles)
	case path == "/queue":
		var records []any
		for _, d := range s.Queue {
			rec := map[string]any{"size": d.Size, "sizeleft": d.Left}
			if s.kind == arr.Radarr {
				rec["movieId"] = d.ArrID
			} else {
				rec["seriesId"] = d.ArrID
			}
			records = append(records, rec)
		}
		write(map[string]any{"records": records})
	default:
		return false
	}
	return true
}

func (s *Server) byExternal(id int64) *Title {
	for _, t := range s.Titles {
		if t.ExternalID == id {
			return t
		}
	}
	return nil
}

// resource is a title as the API returns it; t is nil for a title the instance does not have.
func (s *Server) resource(e Entry, t *Title) map[string]any {
	idField := "tvdbId"
	if s.kind == arr.Radarr {
		idField = "tmdbId"
	}
	var seasons []any
	for _, n := range append([]int{0}, e.Seasons...) {
		seasons = append(seasons, map[string]any{"seasonNumber": n, "monitored": t != nil && slices.Contains(t.MonitoredSeasons, n)})
	}
	m := map[string]any{
		idField: e.ExternalID, "title": e.Title, "year": e.Year, "overview": "About " + e.Title, "seasons": seasons,
		"images": []any{map[string]any{"coverType": "poster", "remoteUrl": e.Poster}},
	}
	if t != nil {
		m["id"] = t.ID
		m["monitored"] = t.Monitored
		m["hasFile"] = t.HasFile
		m["statistics"] = map[string]any{"episodeFileCount": t.EpisodeFiles, "episodeCount": t.EpisodesWanted}
	}
	return m
}
