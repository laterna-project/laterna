// Package naming works out what a file is from its path: a movie (title, year, version, part) or an
// episode (series, season, number). Pure logic, no disk or database access. Paths are relative to
// the library root and use forward slashes.
package naming

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// VideoExtensions lists the extensions treated as video.
var VideoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".wmv": true, ".ts": true,
	".m2ts": true, ".mts": true, ".webm": true, ".mpg": true, ".mpeg": true, ".flv": true, ".ogv": true,
	".3gp": true, ".vob": true, ".iso": false, // disc images are not read directly
}

// IsVideo reports a video file by its extension.
func IsVideo(p string) bool { return VideoExtensions[strings.ToLower(path.Ext(p))] }

// metadataExtensions covers NFO files and images next to media, and the OPF of a Calibre library.
var metadataExtensions = map[string]bool{".nfo": true, ".opf": true, ".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

// IsMetadata reports a local metadata file (NFO, image) by its extension.
func IsMetadata(p string) bool { return metadataExtensions[strings.ToLower(path.Ext(p))] }

// bonusDirs are folders whose content is not a main feature.
var bonusDirs = map[string]bool{
	"extras": true, "featurettes": true, "behind the scenes": true, "deleted scenes": true,
	"interviews": true, "scenes": true, "shorts": true, "trailers": true, "sample": true, "samples": true,
	"bonus": true,
}

// systemDirs are folders created by file systems, NAS boxes and trash bins.
var systemDirs = map[string]bool{
	"@eadir": true, "#recycle": true, "@recycle": true, "$recycle.bin": true, "lost+found": true,
	"system volume information": true, ".trash": true, "#snapshot": true,
}

// SystemDir reports a hidden or system folder (NAS, trash). Never a media folder.
func SystemDir(name string) bool {
	return strings.HasPrefix(name, ".") || systemDirs[strings.ToLower(name)]
}

var ignoredSuffix = regexp.MustCompile(`(?i)[ ._-](sample|trailer|featurette|behindthescenes|deleted|interview|scene|short|extra)$`)

// Ignored reports a path that must not be indexed: hidden file, extra, sample, system folder.
func Ignored(rel string) bool {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ".") || (i < len(parts)-1 && (bonusDirs[strings.ToLower(p)] || systemDirs[strings.ToLower(p)])) {
			return true
		}
	}
	name := strings.TrimSuffix(parts[len(parts)-1], path.Ext(rel))
	return strings.EqualFold(name, "sample") || ignoredSuffix.MatchString(name)
}

// Name cleanup.

var (
	idPattern       = regexp.MustCompile(`(?i)[\[{](tmdb|imdb|tvdb)(?:id)?[-=]((?:tt)?\d+)[\]}]`)
	bracketed       = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	parenYear       = regexp.MustCompile(`\((\d{4})\)`)
	parenthesized   = regexp.MustCompile(`\([^)]*\)`)
	yearToken       = regexp.MustCompile(`^(19\d{2}|20\d{2})$`)
	versionSuffix   = regexp.MustCompile(`\s+-\s+([^-]+?)\s*$`)
	partSuffix      = regexp.MustCompile(`(?i)[\s._-]*(?:cd|dvd|disc|disk|part|pt)[\s._-]*(\d{1,2})$`)
	separatorsOnly  = regexp.MustCompile(`[._]+`)
	multipleSpaces  = regexp.MustCompile(`\s{2,}`)
	trailingJunkSep = regexp.MustCompile(`[\s\-–:,]+$`)
)

// junkTokens mark the end of the title in scene-style names (quality, source, codec...).
var junkTokens = map[string]bool{
	"480p": true, "576p": true, "720p": true, "1080p": true, "1080i": true, "2160p": true, "4k": true, "uhd": true,
	"bluray": true, "blu-ray": true, "bdrip": true, "brrip": true, "bdremux": true, "remux": true, "web-dl": true,
	"webdl": true, "webrip": true, "web": true, "hdtv": true, "dvdrip": true, "hdrip": true, "hdlight": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true, "avc": true, "av1": true, "xvid": true,
	"10bit": true, "8bit": true, "hdr": true, "hdr10": true, "dv": true, "dolby": true, "atmos": true,
	"dts": true, "dts-hd": true, "truehd": true, "aac": true, "ac3": true, "eac3": true, "ddp5": true, "dd5": true,
	"multi": true, "vostfr": true, "vf": true, "vff": true, "vfq": true, "vfi": true, "truefrench": true,
	"french": true, "subfrench": true, "proper": true, "repack": true, "extended": true, "unrated": true,
	"imax": true, "internal": true, "complete": true, "integrale": true, "intégrale": true,
}

// cleaned is a name stripped of its decorations.
type cleaned struct {
	title string
	year  int
	ids   map[string]string
}

// clean pulls the title, year and IDs out of a file or folder name ("Title (2020) [tmdbid-123]",
// "Title.2020.1080p.BluRay.x264-GRP", "[Group] Title").
func clean(name string) cleaned {
	var c cleaned
	for _, m := range idPattern.FindAllStringSubmatch(name, -1) {
		if c.ids == nil {
			c.ids = map[string]string{}
		}
		c.ids[strings.ToLower(m[1])] = m[2]
	}
	name = bracketed.ReplaceAllString(name, " ")
	if m := parenYear.FindAllStringSubmatchIndex(name, -1); len(m) > 0 {
		last := m[len(m)-1]
		c.year, _ = strconv.Atoi(name[last[2]:last[3]])
		name = name[:last[0]]
	}
	name = parenthesized.ReplaceAllString(name, " ")

	// Scene-style name: words separated by dots or underscores.
	if !strings.Contains(strings.TrimSpace(name), " ") || strings.Count(name, ".") >= 2 {
		name = separatorsOnly.ReplaceAllString(name, " ")
	}
	words := strings.Fields(name)
	cut := len(words)
	for i, w := range words {
		lw := strings.ToLower(strings.Trim(w, "-"))
		if junkTokens[lw] || strings.HasPrefix(lw, "ddp") || strings.HasPrefix(lw, "dts") {
			cut = i
			break
		}
	}
	words = words[:cut]
	// Scene-style year: the last year-looking word, unless it is the first word of the title
	// ("2012" and "1917" are titles).
	if c.year == 0 {
		for i := len(words) - 1; i > 0; i-- {
			if yearToken.MatchString(words[i]) {
				c.year, _ = strconv.Atoi(words[i])
				words = words[:i]
				break
			}
		}
	}
	title := strings.Join(words, " ")
	title = multipleSpaces.ReplaceAllString(title, " ")
	c.title = trailingJunkSep.ReplaceAllString(strings.TrimSpace(title), "")
	return c
}

// SortTitle returns the sort key of a title: lower case, without a leading article or common
// accents, so that "Le Parrain" sorts under P and "Été" under E.
func SortTitle(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	for _, article := range []string{"the ", "a ", "an ", "le ", "la ", "les ", "l'", "l’", "un ", "une ", "des "} {
		if strings.HasPrefix(t, article) && len(t) > len(article) {
			t = t[len(article):]
			break
		}
	}
	return foldAccents(t)
}

var accentFold = strings.NewReplacer(
	"à", "a", "â", "a", "ä", "a", "á", "a", "ã", "a", "å", "a", "æ", "ae", "ç", "c", "é", "e", "è", "e",
	"ê", "e", "ë", "e", "í", "i", "ì", "i", "î", "i", "ï", "i", "ñ", "n", "ó", "o", "ò", "o", "ô", "o",
	"ö", "o", "õ", "o", "ø", "o", "œ", "oe", "ú", "u", "ù", "u", "û", "u", "ü", "u", "ý", "y", "ÿ", "y",
)

func foldAccents(s string) string { return accentFold.Replace(s) }

// normalizedKey brings together two spellings of the same title (case, accents, punctuation).
func normalizedKey(title string) string {
	var b strings.Builder
	for _, r := range foldAccents(strings.ToLower(title)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Key returns the normalized form of a title (lower case, no accents or punctuation), to match two
// spellings of the same title.
func Key(title string) string { return normalizedKey(title) }
