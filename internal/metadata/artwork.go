package metadata

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
)

// Artwork is a local image found next to media.
type Artwork struct {
	Kind domain.ImageKind
	Path string
}

var imageExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// candidates lists, for each image kind, the file names we recognize (without extension), most
// specific first. "{base}" stands for the video file name.
type candidates map[domain.ImageKind][]string

var (
	movieNames = candidates{
		domain.ImagePoster:   {"{base}-poster", "poster", "folder", "cover", "movie", "default"},
		domain.ImageBackdrop: {"{base}-fanart", "{base}-backdrop", "fanart", "backdrop", "background"},
		domain.ImageLogo:     {"{base}-clearlogo", "{base}-logo", "clearlogo", "logo"},
		domain.ImageThumb:    {"{base}-landscape", "{base}-thumb", "landscape", "thumb"},
		domain.ImageBanner:   {"{base}-banner", "banner"},
	}
	// A movie sitting at the root of a library only owns the images named after it.
	looseMovieNames = candidates{
		domain.ImagePoster:   {"{base}-poster", "{base}"},
		domain.ImageBackdrop: {"{base}-fanart", "{base}-backdrop"},
		domain.ImageLogo:     {"{base}-clearlogo", "{base}-logo"},
		domain.ImageThumb:    {"{base}-landscape", "{base}-thumb"},
		domain.ImageBanner:   {"{base}-banner"},
	}
	seriesNames = candidates{
		domain.ImagePoster:   {"poster", "folder", "cover", "show", "default"},
		domain.ImageBackdrop: {"fanart", "backdrop", "background"},
		domain.ImageLogo:     {"clearlogo", "logo"},
		domain.ImageThumb:    {"landscape", "thumb"},
		domain.ImageBanner:   {"banner"},
	}
	seasonDirNames = candidates{
		domain.ImagePoster:   {"poster", "folder", "cover"},
		domain.ImageBackdrop: {"fanart", "backdrop"},
		domain.ImageBanner:   {"banner"},
	}
	episodeNames = candidates{
		domain.ImageThumb: {"{base}-thumb", "{base}"},
	}
	// Music: the cover of an album is its (square) poster.
	albumNames = candidates{
		domain.ImagePoster:   {"cover", "folder", "front", "album", "albumart", "poster", "thumb"},
		domain.ImageBackdrop: {"fanart", "backdrop"},
	}
	artistNames = candidates{
		domain.ImagePoster:   {"artist", "folder", "poster", "thumb"},
		domain.ImageBackdrop: {"fanart", "backdrop", "background"},
		domain.ImageLogo:     {"clearlogo", "logo"},
		domain.ImageBanner:   {"banner"},
	}
)

// MovieArtwork finds the images of a movie. ownFolder means the movie has a folder to itself, so
// the generic images ("poster.jpg"...) belong to it.
func MovieArtwork(dir, base string, ownFolder bool) ([]Artwork, error) {
	names := movieNames
	if !ownFolder {
		names = looseMovieNames
	}
	return find(dir, base, names)
}

// SeriesArtwork finds the images of a series in its folder.
func SeriesArtwork(seriesDir string) ([]Artwork, error) { return find(seriesDir, "", seriesNames) }

// SeasonArtwork finds the images of a season: in the series folder ("season01-poster.jpg",
// "season-specials-poster.jpg"), then in the season folder.
func SeasonArtwork(seriesDir, seasonDir string, season int) ([]Artwork, error) {
	prefix := fmt.Sprintf("season%02d", season)
	if season == 0 {
		prefix = "season-specials"
	}
	inSeries := candidates{
		domain.ImagePoster:   {prefix + "-poster", prefix},
		domain.ImageBackdrop: {prefix + "-fanart"},
		domain.ImageBanner:   {prefix + "-banner"},
		domain.ImageThumb:    {prefix + "-landscape"},
	}
	found, err := find(seriesDir, "", inSeries)
	if err != nil || seasonDir == "" || filepath.Clean(seasonDir) == filepath.Clean(seriesDir) {
		return found, err
	}
	more, err := find(seasonDir, "", seasonDirNames)
	return mergeArtwork(found, more), err
}

// EpisodeArtwork finds the thumbnail of an episode ("<file>-thumb.jpg").
func EpisodeArtwork(dir, base string) ([]Artwork, error) { return find(dir, base, episodeNames) }

// AlbumArtwork finds the images of an album in its folders, from the one closest to the tracks (a
// disc folder) up to the album folder.
func AlbumArtwork(dirs ...string) ([]Artwork, error) {
	var out []Artwork
	for _, dir := range dirs {
		found, err := find(dir, "", albumNames)
		if err != nil {
			return out, err
		}
		out = mergeArtwork(out, found)
	}
	return out, nil
}

// ArtistArtwork finds the images of an artist in its folder.
func ArtistArtwork(artistDir string) ([]Artwork, error) { return find(artistDir, "", artistNames) }

// find looks, for each kind, for the first candidate name present in dir, ignoring case.
func find(dir, base string, names candidates) ([]Artwork, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{} // lower-case name -> real name
	for _, e := range entries {
		if !e.IsDir() {
			files[strings.ToLower(e.Name())] = e.Name()
		}
	}
	var out []Artwork
	for _, kind := range domain.ImageKinds {
		for _, candidate := range names[kind] {
			name := strings.ToLower(strings.ReplaceAll(candidate, "{base}", base))
			if name == "" {
				continue
			}
			if actual, ok := lookup(files, name); ok {
				out = append(out, Artwork{Kind: kind, Path: filepath.Join(dir, actual)})
				break
			}
		}
	}
	return out, nil
}

func lookup(files map[string]string, name string) (string, bool) {
	for _, ext := range imageExts {
		if actual, ok := files[name+ext]; ok {
			return actual, true
		}
	}
	return "", false
}

// mergeArtwork keeps the first image found for each kind.
func mergeArtwork(first, second []Artwork) []Artwork {
	have := map[domain.ImageKind]bool{}
	for _, a := range first {
		have[a.Kind] = true
	}
	for _, a := range second {
		if !have[a.Kind] {
			first = append(first, a)
		}
	}
	return first
}

// NFOPath returns the NFO of an item if there is one: the first candidate that exists.
func NFOPath(candidates ...string) string {
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}
