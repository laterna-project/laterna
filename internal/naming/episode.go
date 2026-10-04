package naming

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Episode is what a path says about an episode.
type Episode struct {
	// SeriesDir is the series folder (first level under the root), "" for a file sitting at the
	// root.
	SeriesDir   string
	SeriesTitle string
	SeriesYear  int
	SeriesIDs   map[string]string
	// Season is -1 when neither the folder nor the name gives it (absolute numbering).
	Season  int
	Episode int
	// EpisodeEnd is the last episode of a multi-episode file, 0 otherwise.
	EpisodeEnd int
	// Absolute means numbering without seasons ("Title - 12"), common for anime.
	Absolute bool
	// AbsoluteNumber is the episode number counted from the start of the series: written next to
	// SxxEyy ("S03E13 (054)", as Sonarr does), or Episode itself with absolute numbering. 0 if
	// unknown.
	AbsoluteNumber int
	// Title is the episode title found in the name, if any.
	Title string
}

var (
	seasonEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,3})[ ._-]?e(\d{1,4})(?:[ ._-]?-?[ ._-]?e(\d{1,4})|-(\d{1,4})(?:[^0-9]|$))?`)
	crossNotation = regexp.MustCompile(`(?:^|[^0-9])(\d{1,2})x(\d{2,3})(?:[^0-9]|$)`)
	episodeWord   = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:episode|[eé]pisode|ep|e)[ ._-]?(\d{1,4})(?:[^0-9]|$)`)
	absoluteDash  = regexp.MustCompile(`\s-\s#?(\d{1,4})(?:v\d)?(?:\s|$)`)
	// numberDash matches "Show 01 - Episode title" (fansubs put the number after the show name).
	numberDash    = regexp.MustCompile(`\s#?(\d{1,4})(?:v\d)?\s+-\s`)
	bareNumber    = regexp.MustCompile(`^#?(\d{1,4})(?:v\d)?$`)
	leadingNumber = regexp.MustCompile(`^#?(\d{1,4})(?:v\d)?\s*[-.]\s`)
	trailingNum   = regexp.MustCompile(`\s#?(\d{1,4})(?:v\d)?$`)
	seasonDir     = regexp.MustCompile(`(?i)^(?:season|saison|staffel|temporada|stagione|series|s)[ ._-]*(\d{1,3})\b`)
	specialsDir   = regexp.MustCompile(`(?i)^(?:specials?|sp[ée]ciaux|sp[ée]cial)$`)
	// qualitySuffix matches the quality Sonarr appends ("WEBDL-1080p", "HDTV-720p Proper").
	qualitySuffix = regexp.MustCompile(`(?i)\s+(?:web-?dl|webrip|hdtv|bluray|bdrip|dvd|sdtv|raw-hd|remux)(?:-\d{3,4}p)?(?:\s+(?:proper|repack))?\s*$`)
	// absoluteAfter matches an absolute number in parentheses right after SxxEyy ("S03E13 (054)").
	absoluteAfter = regexp.MustCompile(`^\s*[-–]?\s*\((\d{1,4})\)`)
)

// ParseEpisode parses the path of a video file in a series library: "Show (Year)/Season 01/Show
// S01E02.mkv", "Show/S01E02 - Title.mkv", "Show.S01E02.1080p.mkv" at the root, "[Group] Anime - 12
// [1080p].mkv"...
func ParseEpisode(rel string) (Episode, bool) {
	parts := strings.Split(rel, "/")
	base := strings.TrimSuffix(parts[len(parts)-1], path.Ext(rel))
	e := Episode{Season: -1}

	if len(parts) >= 2 {
		e.SeriesDir = parts[0]
		series := clean(parts[0])
		e.SeriesTitle, e.SeriesYear, e.SeriesIDs = series.title, series.year, series.ids
		// Season folder: the last folder before the file, if it looks like one.
		if len(parts) >= 3 {
			if n, ok := seasonFromDir(parts[len(parts)-2]); ok {
				e.Season = n
			}
		}
	}

	name := bracketed.ReplaceAllString(base, " ")
	if loc := seasonEpisode.FindStringSubmatchIndex(name); loc != nil {
		e.Season = atoi(name[loc[2]:loc[3]])
		e.Episode = atoi(name[loc[4]:loc[5]])
		e.EpisodeEnd = firstGroup(name, loc, 6, 8)
		rest := name[loc[1]:]
		if m := absoluteAfter.FindStringSubmatchIndex(rest); m != nil {
			e.AbsoluteNumber = atoi(rest[m[2]:m[3]])
			rest = rest[m[1]:]
		}
		e.Title = episodeTitle(rest)
		e.fillSeriesFromName(name[:loc[0]])
		return e, e.valid()
	}
	if loc := crossNotation.FindStringSubmatchIndex(name); loc != nil {
		e.Season = atoi(name[loc[2]:loc[3]])
		e.Episode = atoi(name[loc[4]:loc[5]])
		e.Title = episodeTitle(name[loc[1]:])
		e.fillSeriesFromName(name[:loc[0]])
		return e, e.valid()
	}

	// Absolute numbering: drop the series title and anything in parentheses first.
	flat := strings.TrimSpace(parenthesized.ReplaceAllString(name, " "))
	flat = multipleSpaces.ReplaceAllString(separatorsOnlyIfScene(flat), " ")
	for _, re := range []*regexp.Regexp{bareNumber, leadingNumber, absoluteDash, numberDash, episodeWord, trailingNum} {
		loc := re.FindStringSubmatchIndex(flat)
		if loc == nil {
			continue
		}
		n := atoi(flat[loc[2]:loc[3]])
		if isYearOrResolution(n, flat[loc[3]:]) {
			continue
		}
		e.Episode = n
		e.Absolute = e.Season < 0
		if e.Absolute {
			e.Season = 1
			e.AbsoluteNumber = n
		}
		e.Title = episodeTitle(flat[loc[1]:])
		e.fillSeriesFromName(flat[:loc[0]])
		return e, e.valid()
	}
	return e, false
}

// fillSeriesFromName takes the series from the file name when there is no folder.
func (e *Episode) fillSeriesFromName(prefix string) {
	if e.SeriesTitle != "" {
		return
	}
	c := clean(prefix)
	e.SeriesTitle, e.SeriesYear, e.SeriesIDs = c.title, c.year, c.ids
}

func (e *Episode) valid() bool {
	if e.EpisodeEnd != 0 && e.EpisodeEnd <= e.Episode {
		e.EpisodeEnd = 0
	}
	return e.Episode > 0 || e.Season == 0
}

func seasonFromDir(name string) (int, bool) {
	if specialsDir.MatchString(strings.TrimSpace(name)) {
		return 0, true
	}
	if m := seasonDir.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		return atoi(m[1]), true
	}
	return 0, false
}

// episodeTitle cleans what follows the number: "- The Title", ".The.Title.1080p.WEB".
func episodeTitle(rest string) string {
	rest = strings.TrimLeft(rest, " ._-–:")
	rest = qualitySuffix.ReplaceAllString(rest, "")
	return clean(rest).title
}

func separatorsOnlyIfScene(s string) string {
	if strings.Count(s, ".") >= 2 || !strings.Contains(s, " ") {
		return separatorsOnly.ReplaceAllString(s, " ")
	}
	return s
}

// isYearOrResolution rules out "1080p", "720", "2020" mistaken for episode numbers.
func isYearOrResolution(n int, after string) bool {
	if strings.HasPrefix(strings.ToLower(after), "p") || strings.HasPrefix(strings.ToLower(after), "i") {
		return true
	}
	switch n {
	case 480, 576, 720, 1080, 2160:
		return true
	}
	return n >= 1900 && n <= 2099
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// firstGroup returns the first non-empty capture among the given groups.
func firstGroup(s string, loc []int, groups ...int) int {
	for _, g := range groups {
		if loc[g] >= 0 {
			return atoi(s[loc[g]:loc[g+1]])
		}
	}
	return 0
}
