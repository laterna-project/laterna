package naming

import (
	"path"
	"regexp"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
)

// Extra is an extra recognized by its path: in a folder of extras ("Trailers/", "Behind The
// Scenes/") or named after its movie with a suffix ("Movie (2020)-trailer.mkv"), the way Plex,
// Jellyfin, Kodi and tinyMediaManager lay them out.
type Extra struct {
	Kind domain.ExtraKind
	// Title is the name of the file, without the suffix of its kind, cleaned up.
	Title string
	// Owner is the folder of the movie or series it goes with ("." at the root). OwnerName is the
	// name of the movie file it is named after ("Movie (2020)" for "Movie (2020)-trailer.mkv"),
	// empty for an extra in a folder of extras.
	Owner     string
	OwnerName string
}

// extraDirs are the folders of extras, and the kind of what they hold.
var extraDirs = map[string]domain.ExtraKind{
	"trailers":          domain.ExtraTrailer,
	"featurettes":       domain.ExtraFeaturette,
	"behind the scenes": domain.ExtraBehindTheScenes,
	"deleted scenes":    domain.ExtraDeletedScene,
	"interviews":        domain.ExtraInterview,
	"scenes":            domain.ExtraScene,
	"shorts":            domain.ExtraShort,
	"extras":            domain.ExtraOther,
	"extra":             domain.ExtraOther,
	"bonus":             domain.ExtraOther,
	"other":             domain.ExtraOther,
	"others":            domain.ExtraOther,
	"clips":             domain.ExtraOther,
}

// extraSuffixes are the suffixes of an extra named after its movie, and their kind.
var extraSuffixes = map[string]domain.ExtraKind{
	"trailer":         domain.ExtraTrailer,
	"featurette":      domain.ExtraFeaturette,
	"behindthescenes": domain.ExtraBehindTheScenes,
	"deleted":         domain.ExtraDeletedScene,
	"interview":       domain.ExtraInterview,
	"scene":           domain.ExtraScene,
	"short":           domain.ExtraShort,
	"other":           domain.ExtraOther,
	"extra":           domain.ExtraOther,
	"clip":            domain.ExtraOther,
}

// extraSuffix matches "-trailer", "-trailer2", "-featurette" at the end of a file name. A dot or a
// space is not enough: "The.Big.Short" is a movie. A release group can still look like one
// ("Movie.2015.BluRay-SCENE"): the caller takes it as an extra only if a movie is named OwnerName.
var extraSuffix = regexp.MustCompile(`(?i)^(.+?)-(trailer|featurette|behindthescenes|deleted|interview|scene|short|other|extra|clip)\d*$`)

// ParseExtra recognizes an extra by its path (relative to the library root). The deepest folder
// of extras decides; the folders above it are those of its movie or series.
func ParseExtra(rel string) (Extra, bool) {
	parts := strings.Split(rel, "/")
	base := strings.TrimSuffix(parts[len(parts)-1], path.Ext(rel))
	for i := len(parts) - 2; i >= 0; i-- {
		if kind, ok := extraDirs[strings.ToLower(parts[i])]; ok {
			owner := strings.Join(parts[:i], "/")
			if owner == "" {
				owner = "."
			}
			return Extra{Kind: kind, Title: extraTitle(base), Owner: owner}, true
		}
	}
	m := extraSuffix.FindStringSubmatch(base)
	if m == nil {
		return Extra{}, false
	}
	return Extra{
		Kind: extraSuffixes[strings.ToLower(m[2])], Title: extraTitle(base), Owner: path.Dir(rel), OwnerName: m[1],
	}, true
}

// IsExtra reports a path that ParseExtra recognizes.
func IsExtra(rel string) bool {
	_, ok := ParseExtra(rel)
	return ok
}

// extraTitle is the name of an extra as shown: its file name, dots and underscores turned into
// spaces ("Making of", "Movie (2020)-trailer").
func extraTitle(base string) string {
	if t := strings.TrimSpace(multipleSpaces.ReplaceAllString(separatorsOnly.ReplaceAllString(base, " "), " ")); t != "" {
		return t
	}
	return base
}
