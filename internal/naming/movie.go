package naming

import (
	"path"
	"strconv"
	"strings"
)

// Movie is what a path says about a movie.
type Movie struct {
	Title string
	Year  int
	// Version tells apart several files of the same movie ("1080p", "Director's Cut").
	Version string
	// Part numbers the files of a movie split in several (cd1, part2...); 0 for a single file.
	Part int
	// IDs are provider IDs found in the name (tmdb, imdb, tvdb).
	IDs map[string]string
	// GroupKey groups the files of one movie (versions, parts): folder plus normalized title.
	GroupKey string
}

// ParseMovie parses the path of a video file in a movie library. Inside a movie folder ("Title
// (Year)/...") the folder decides the title and year. A file at the root is a movie on its own.
func ParseMovie(rel string) Movie {
	dir := path.Dir(rel)
	base := strings.TrimSuffix(path.Base(rel), path.Ext(rel))

	var m Movie
	if n, ok := partNumber(base); ok {
		m.Part = n
		base = partSuffix.ReplaceAllString(base, "")
	}
	// Disc folder ("CD1", "Disc 2"): the movie is the parent folder.
	if n, ok := partNumber(path.Base(dir)); ok && isOnlyPart(path.Base(dir)) {
		m.Part = n
		dir = path.Dir(dir)
	}

	fileName, version := splitVersion(base)
	file := clean(fileName)
	m.Version = version
	m.IDs = file.ids

	if dir == "." || dir == "" {
		m.Title, m.Year = file.title, file.year
		m.GroupKey = "/" + normalizedKey(m.Title) + yearKey(m.Year)
		return m
	}
	folder := clean(path.Base(dir))
	m.Title, m.Year = folder.title, folder.year
	if m.Title == "" {
		m.Title = file.title
	}
	if m.Year == 0 {
		m.Year = file.year
	}
	if folder.ids != nil {
		if m.IDs == nil {
			m.IDs = map[string]string{}
		}
		for k, v := range folder.ids {
			m.IDs[k] = v
		}
	}
	// Several movies in one folder stay separate: group by folder AND by file title (without
	// version or part).
	m.GroupKey = dir + "/" + normalizedKey(file.title)
	return m
}

func yearKey(y int) string {
	if y == 0 {
		return ""
	}
	return "-" + strconv.Itoa(y)
}

// splitVersion splits "Title (2017) - 1080p" into title and version. A dash with no year before it
// stays in the title ("Mission: Impossible - Fallout").
func splitVersion(base string) (string, string) {
	loc := versionSuffix.FindStringSubmatchIndex(base)
	if loc == nil {
		return base, ""
	}
	head := base[:loc[0]]
	if !parenYear.MatchString(head) && !strings.ContainsAny(head, "[{") {
		return base, ""
	}
	return head, strings.TrimSpace(base[loc[2]:loc[3]])
}

func partNumber(s string) (int, bool) {
	m := partSuffix.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n > 0
}

func isOnlyPart(name string) bool {
	return strings.TrimSpace(partSuffix.ReplaceAllString(name, "")) == ""
}
