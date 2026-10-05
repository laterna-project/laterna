package arr

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Event is what a Sonarr or Radarr webhook tells Laterna.
type Event struct {
	// Type is "Download", "Rename", "SeriesDelete", "Test"...
	Type string
	// Path is the folder of the series or movie concerned, as the instance sees it ("" if missing).
	Path string
}

// ParseEvent reads the body of a webhook.
func ParseEvent(data []byte) (Event, error) {
	var raw struct {
		EventType string `json:"eventType"`
		Series    *struct {
			Path string `json:"path"`
		} `json:"series"`
		Movie *struct {
			FolderPath string `json:"folderPath"`
		} `json:"movie"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Event{}, fmt.Errorf("unreadable webhook: %w", err)
	}
	if raw.EventType == "" {
		return Event{}, fmt.Errorf("webhook without \"eventType\"")
	}
	e := Event{Type: raw.EventType}
	switch {
	case raw.Series != nil:
		e.Path = raw.Series.Path
	case raw.Movie != nil:
		e.Path = raw.Movie.FolderPath
	}
	return e, nil
}

// changesFiles lists the events after which files (media, NFO, images) have changed.
var changesFiles = map[string]bool{
	"Download": true, "ImportComplete": true, "Rename": true,
	"SeriesDelete": true, "EpisodeFileDelete": true, "MovieDelete": true, "MovieFileDelete": true,
}

// ChangesFiles reports an event that calls for a new scan.
func (e Event) ChangesFiles() bool { return changesFiles[e.Type] }

// MapPath maps a folder as Sonarr or Radarr sees it (often inside a container: "/tv/Anime/Dr.
// STONE") to the folder Laterna sees ("D:\media\tv\Anime\Dr. STONE"). No setting is needed: the
// path is used as is if it is under a root, otherwise we take the longest tail of the path that
// exists under one of the roots. exists checks that a folder exists.
func MapPath(arrPath string, roots []string, exists func(string) bool) (string, bool) {
	arrPath = strings.TrimSpace(arrPath)
	if arrPath == "" {
		return "", false
	}
	if native := filepath.Clean(arrPath); filepath.IsAbs(native) {
		for _, r := range roots {
			if under(native, r) && exists(native) {
				return native, true
			}
		}
	}
	parts := strings.FieldsFunc(arrPath, func(r rune) bool { return r == '/' || r == '\\' })
	for i := range parts {
		for _, r := range roots {
			candidate := filepath.Join(append([]string{r}, parts[i:]...)...)
			if under(candidate, r) && exists(candidate) {
				return candidate, true
			}
		}
	}
	return "", false
}

// under reports whether p is inside dir, or is dir.
func under(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
