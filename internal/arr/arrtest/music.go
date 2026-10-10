package arrtest

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/laterna-project/laterna/internal/arr"
)

// MusicEntry is an artist a Lidarr search can find, with its albums.
type MusicEntry struct {
	MBID   string
	Name   string
	Poster string
	Albums []AlbumEntry
}

// AlbumEntry is an album of an artist.
type AlbumEntry struct {
	MBID  string
	Title string
	// Type is "Album" if empty.
	Type string
	// Date is the release date ("2013-05-17").
	Date  string
	Cover string
}

// Artist is an artist Lidarr has.
type Artist struct {
	MusicEntry
	ID                int
	Monitored         bool
	MonitorNewItems   string
	RootFolder        string
	QualityProfileID  int
	MetadataProfileID int
}

// Album is an album Lidarr has.
type Album struct {
	AlbumEntry
	ID, ArtistID int
	Monitored    bool
	TrackFiles   int
	Tracks       int
}

// serveMusic answers Lidarr's searches, artists, albums and metadata profiles. It returns false
// for anything else.
func (s *Server) serveMusic(w http.ResponseWriter, r *http.Request, path string, body map[string]any, write func(any)) bool {
	q := r.URL.Query()
	switch {
	case path == "/search":
		term := strings.ToLower(q.Get("term"))
		var list []any
		for _, e := range s.Music {
			if strings.Contains(strings.ToLower(e.Name), term) {
				list = append(list, map[string]any{"foreignId": e.MBID, "artist": s.artistResource(e)})
			}
			for _, al := range e.Albums {
				if strings.Contains(strings.ToLower(al.Title), term) {
					list = append(list, map[string]any{"foreignId": al.MBID, "album": s.albumResource(e, al)})
				}
			}
		}
		write(list)
	case path == "/artist/lookup" || path == "/album/lookup":
		mbid := strings.TrimPrefix(q.Get("term"), "lidarr:")
		var list []any
		for _, e := range s.Music {
			if path == "/artist/lookup" && e.MBID == mbid {
				list = append(list, s.artistResource(e))
			}
			for _, al := range e.Albums {
				if path == "/album/lookup" && al.MBID == mbid {
					list = append(list, s.albumResource(e, al))
				}
			}
		}
		write(list)
	case path == "/metadataprofile":
		write(s.MetadataProfiles)
	case path == "/artist" && r.Method == http.MethodGet && q.Get("mbId") == "":
		var list []any
		for _, f := range s.Folders {
			n := 0
			if f.HasFiles {
				n = 1
			}
			list = append(list, map[string]any{"artistName": f.Title, "path": f.Path, "statistics": map[string]any{"trackFileCount": n}})
		}
		write(list)
	case path == "/artist" && r.Method == http.MethodGet:
		var list []any
		if a := s.artistByMBID(q.Get("mbId")); a != nil {
			list = append(list, s.artistResource(a.MusicEntry))
		}
		write(list)
	case path == "/artist" && r.Method == http.MethodPost:
		if !s.validTarget(body) {
			w.WriteHeader(http.StatusBadRequest)
			write([]any{map[string]any{"propertyName": "RootFolderPath", "errorMessage": "Invalid root folder or profile"}})
			return true
		}
		mbid, _ := body["foreignArtistId"].(string)
		monitor := "all"
		if opts, ok := body["addOptions"].(map[string]any); ok {
			monitor, _ = opts["monitor"].(string)
		}
		a := s.addArtist(mbid, body, monitor)
		write(s.artistResource(a.MusicEntry))
	case strings.HasPrefix(path, "/artist/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(path, "/artist/"))
		a := s.Artists[id]
		if a == nil {
			http.NotFound(w, r)
			return true
		}
		if r.Method == http.MethodPut {
			a.Monitored = body["monitored"] == true
			a.MonitorNewItems, _ = body["monitorNewItems"].(string)
		}
		write(s.artistResource(a.MusicEntry))
	case path == "/album" && r.Method == http.MethodGet:
		var list []any
		for _, id := range s.albumIDs() {
			al := s.Albums[id]
			if al == nil {
				continue
			}
			if q.Get("foreignAlbumId") != "" && al.MBID != q.Get("foreignAlbumId") {
				continue
			}
			if q.Get("artistId") != "" && strconv.Itoa(al.ArtistID) != q.Get("artistId") {
				continue
			}
			list = append(list, s.albumResource(s.artistEntry(al.ArtistID), al.AlbumEntry))
		}
		write(list)
	case path == "/album" && r.Method == http.MethodPost:
		artist, _ := body["artist"].(map[string]any)
		if artist == nil || !s.validTarget(artist) {
			w.WriteHeader(http.StatusBadRequest)
			write([]any{map[string]any{"propertyName": "RootFolderPath", "errorMessage": "Invalid root folder or profile"}})
			return true
		}
		mbid, _ := body["foreignAlbumId"].(string)
		artistMBID, _ := artist["foreignArtistId"].(string)
		a := s.artistByMBID(artistMBID)
		if a == nil {
			a = s.addArtist(artistMBID, artist, "none")
		}
		al := s.albumByMBID(mbid)
		if al != nil {
			al.Monitored = true
			write(s.albumResource(a.MusicEntry, al.AlbumEntry))
		}
	case path == "/album/monitor" && r.Method == http.MethodPut:
		ids, _ := body["albumIds"].([]any)
		for _, x := range ids {
			n, _ := x.(float64)
			if al := s.Albums[int(n)]; al != nil {
				al.Monitored = body["monitored"] == true
			}
		}
	case strings.HasPrefix(path, "/album/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(path, "/album/"))
		al := s.Albums[id]
		if al == nil {
			http.NotFound(w, r)
			return true
		}
		write(s.albumResource(s.artistEntry(al.ArtistID), al.AlbumEntry))
	default:
		return false
	}
	return true
}

func (s *Server) validTarget(body map[string]any) bool {
	profile, _ := body["qualityProfileId"].(float64)
	meta, _ := body["metadataProfileId"].(float64)
	root, _ := body["rootFolderPath"].(string)
	return slices.ContainsFunc(s.Profiles, func(p arr.QualityProfile) bool { return p.ID == int(profile) }) &&
		slices.ContainsFunc(s.MetadataProfiles, func(p arr.QualityProfile) bool { return p.ID == int(meta) }) &&
		slices.ContainsFunc(s.Roots, func(f arr.RootFolder) bool { return f.Path == root })
}

// addArtist adds an artist and all its albums, monitored as addOptions.monitor says.
func (s *Server) addArtist(mbid string, body map[string]any, monitor string) *Artist {
	var entry MusicEntry
	for _, e := range s.Music {
		if e.MBID == mbid {
			entry = e
		}
	}
	profile, _ := body["qualityProfileId"].(float64)
	meta, _ := body["metadataProfileId"].(float64)
	root, _ := body["rootFolderPath"].(string)
	newItems, _ := body["monitorNewItems"].(string)
	a := &Artist{
		MusicEntry: entry, ID: len(s.Artists) + 1, Monitored: body["monitored"] == true, MonitorNewItems: newItems,
		RootFolder: root, QualityProfileID: int(profile), MetadataProfileID: int(meta),
	}
	s.Artists[a.ID] = a
	for i, al := range entry.Albums {
		monitored := monitor == "all" || (monitor == "first" && i == 0) || (monitor == "latest" && i == len(entry.Albums)-1)
		id := len(s.Albums) + 1
		s.Albums[id] = &Album{AlbumEntry: al, ID: id, ArtistID: a.ID, Monitored: monitored, Tracks: 10}
	}
	return a
}

// artistEntry is the entry of an artist Lidarr has, empty if it has none.
func (s *Server) artistEntry(id int) MusicEntry {
	if a := s.Artists[id]; a != nil {
		return a.MusicEntry
	}
	return MusicEntry{}
}

func (s *Server) artistByMBID(mbid string) *Artist {
	for _, a := range s.Artists {
		if a.MBID == mbid {
			return a
		}
	}
	return nil
}

func (s *Server) albumByMBID(mbid string) *Album {
	for _, al := range s.Albums {
		if al.MBID == mbid {
			return al
		}
	}
	return nil
}

func (s *Server) albumIDs() []int {
	ids := make([]int, 0, len(s.Albums))
	for id := range s.Albums {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// artistResource is an artist as the API returns it, with its ID and statistics if Lidarr has it.
func (s *Server) artistResource(e MusicEntry) map[string]any {
	m := map[string]any{
		"foreignArtistId": e.MBID, "artistName": e.Name, "overview": "About " + e.Name, "artistType": "Group",
		"images": []any{map[string]any{"coverType": "poster", "remoteUrl": e.Poster}},
	}
	if a := s.artistByMBID(e.MBID); a != nil {
		files, tracks := 0, 0
		for _, al := range s.Albums {
			if al.ArtistID == a.ID && al.Monitored {
				files, tracks = files+al.TrackFiles, tracks+al.Tracks
			}
		}
		m["id"], m["monitored"], m["monitorNewItems"] = a.ID, a.Monitored, a.MonitorNewItems
		m["statistics"] = map[string]any{"trackFileCount": files, "trackCount": tracks}
	}
	return m
}

// albumResource is an album as the API returns it, with its artist, and its ID and statistics if
// Lidarr has it.
func (s *Server) albumResource(e MusicEntry, al AlbumEntry) map[string]any {
	kind := al.Type
	if kind == "" {
		kind = "Album"
	}
	m := map[string]any{
		"foreignAlbumId": al.MBID, "title": al.Title, "albumType": kind, "releaseDate": al.Date + "T00:00:00Z",
		"images": []any{map[string]any{"coverType": "cover", "remoteUrl": al.Cover}},
		"artist": s.artistResource(e),
	}
	if t := s.albumByMBID(al.MBID); t != nil {
		m["id"], m["artistId"], m["monitored"] = t.ID, t.ArtistID, t.Monitored
		tracks := 0
		if t.Monitored {
			tracks = t.Tracks
		}
		m["statistics"] = map[string]any{"trackFileCount": t.TrackFiles, "trackCount": tracks}
	}
	return m
}
