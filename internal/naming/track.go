package naming

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// AudioExtensions lists the formats indexed in a music library.
var AudioExtensions = map[string]bool{
	".flac": true, ".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".oga": true, ".opus": true,
	".wav": true, ".wma": true, ".aiff": true, ".aif": true, ".ape": true, ".wv": true, ".mka": true,
	".dsf": true, ".dff": true, ".alac": true,
}

// IsAudio reports an audio file by its extension.
func IsAudio(p string) bool { return AudioExtensions[strings.ToLower(path.Ext(p))] }

// Track is what the path of an audio file says about its track. The file's tags win; this is only a
// fallback for untagged files.
type Track struct {
	Title string
	// Disc and Number; 0 if the name does not say.
	Disc, Number int
	// Album and Year come from the album folder ("Album (2020)"). Album is empty for a file at the
	// root.
	Album string
	Year  int
	// Artist comes from the artist folder (parent of the album folder) or from the album folder
	// name ("Artist - Album (2020)"); empty otherwise.
	Artist string
}

var (
	// "1-04 Title", "01.04 - Title": disc and number.
	discTrack = regexp.MustCompile(`^(\d{1,2})[-.](\d{1,3})(?:\s*[-.]\s*|\s+)(.+)$`)
	// "04 - Title", "04. Title", "04 Title".
	trackOnly = regexp.MustCompile(`^(\d{1,3})(?:\s*[-.]\s*|\s+)(.+)$`)
	// Disc folder: "CD1", "Disc 2", "Disque 1".
	discDir = regexp.MustCompile(`(?i)^(?:cd|disc|disk|disque)\s*(\d{1,2})$`)
)

// IsDiscDir reports the folder of one disc of an album ("CD1", "Disc 2", "Disque 1").
func IsDiscDir(name string) bool { return discDir.MatchString(name) }

// ParseTrack reads a path relative to the library root (with forward slashes): "Artist/Album
// (2020)/01 - Title.flac", "Artist - Album - 1-04 Title.flac", "Album/CD2/03 Title.mp3".
func ParseTrack(rel string) Track {
	var t Track
	base := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	t.Title = base
	// The number is at the start of the first segment that begins with a digit. What comes before
	// (artist, album) is dropped, what comes after is part of the title.
	segments := strings.Split(base, " - ")
	for i, seg := range segments {
		rest := strings.Join(append([]string{seg}, segments[i+1:]...), " - ")
		if m := discTrack.FindStringSubmatch(rest); m != nil {
			t.Disc, _ = strconv.Atoi(m[1])
			t.Number, _ = strconv.Atoi(m[2])
			t.Title = strings.TrimSpace(m[3])
			break
		}
		if m := trackOnly.FindStringSubmatch(rest); m != nil {
			t.Number, _ = strconv.Atoi(m[1])
			t.Title = strings.TrimSpace(m[2])
			break
		}
	}

	dirs := strings.Split(path.Dir(rel), "/")
	if len(dirs) == 1 && dirs[0] == "." {
		return t
	}
	if m := discDir.FindStringSubmatch(dirs[len(dirs)-1]); m != nil {
		if t.Disc == 0 {
			t.Disc, _ = strconv.Atoi(m[1])
		}
		dirs = dirs[:len(dirs)-1]
		if len(dirs) == 0 {
			return t
		}
	}
	album := clean(dirs[len(dirs)-1])
	t.Album, t.Year = album.title, album.year
	if len(dirs) >= 2 {
		t.Artist = clean(dirs[len(dirs)-2]).title
	} else if artist, title, ok := strings.Cut(album.title, " - "); ok {
		t.Artist, t.Album = strings.TrimSpace(artist), strings.TrimSpace(title)
	}
	return t
}
