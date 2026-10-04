package naming

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// BookExtensions lists the book formats we know.
var BookExtensions = map[string]bool{".epub": true, ".pdf": true, ".cbz": true}

// IsBook reports a book file by its extension.
func IsBook(p string) bool { return BookExtensions[strings.ToLower(path.Ext(p))] }

// PhotoExtensions lists the photo formats we know. HEIC and AVIF are not supported yet.
var PhotoExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}

// IsPhoto reports a photo by its extension.
func IsPhoto(p string) bool { return PhotoExtensions[strings.ToLower(path.Ext(p))] }

// Book is what the path of a book file says about it. What is inside the file wins; this is only a
// fallback.
type Book struct {
	// Title is the cleaned name of a standalone book. Empty for a volume: the text after the number
	// is too often an author or a release group.
	Title string
	// Series and Number of a volume. HasNumber tells volume 0 from a name without a number.
	Series    string
	Number    float64
	HasNumber bool
	Year      int
}

var (
	// Groups in brackets or braces: teams, formats, versions ("[Team X]", "[V1]").
	bookBracketsRe = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	bookParensRe   = regexp.MustCompile(`\(([^)]*)\)`)
	// Group tag at the end of a release-style name: "...FR.[CBZ]-TONER-Paprika+".
	bookGroupRe = regexp.MustCompile(`[.\]]?-[\p{L}\p{N}+]+(?:-[\p{L}\p{N}+]+)*$`)
	bookYearRe  = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	bookSpaceRe = regexp.MustCompile(`\s+`)
	// Words that say nothing about the book: languages, formats, scan tags.
	bookTags = map[string]bool{
		"fr": true, "vf": true, "vo": true, "french": true, "francais": true, "en": true, "eng": true, "english": true,
		"cbz": true, "cbr": true, "pdf": true, "epub": true, "hd": true, "digital": true, "scan": true, "c2c": true,
		"ebook": true, "notag": true,
	}
	bookVersionRe = regexp.MustCompile(`(?i)^v\d$`)
	// Volume markers, from the most to the least reliable.
	bookMarkers = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:^|[\s,.(-])(?:tome|tomo|volume|vol\.?|livre|book|n°|no\.)\s*#?\s*(\d+(?:[.,]\d+)?)\b`),
		regexp.MustCompile(`(?i)(?:^|[\s,.(-])[tv](\d+(?:[.,]\d+)?)\b`),
		regexp.MustCompile(`#\s*(\d+(?:[.,]\d+)?)\b`),
		// "Astérix - 01 - Astérix le Gaulois"
		regexp.MustCompile(`(?:^|\s)-\s*(\d{1,4}(?:[.,]\d+)?)\s*(?:-|$)`),
		// "001", "01 - Title" (the folder gives the series)
		regexp.MustCompile(`^(\d{1,4}(?:[.,]\d+)?)(?:\s|$)`),
	}
	// "Naruto 12": only inside a folder of the same name ("Fahrenheit 451" is not a volume).
	bookTrailingRe = regexp.MustCompile(`^(.+?)\s+(\d{1,4}(?:[.,]\d+)?)$`)
)

// ParseBook parses the path of a book file (relative to the root, with forward slashes): "One Piece
// - Tome #105", "One.Piece.T104.Oda.FR.[CBZ]-TONER", "Lakestone T1 - Author", "Naruto/001.cbz".
func ParseBook(rel string) Book {
	name := strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	parent := ""
	if dir := path.Dir(rel); dir != "." {
		parent = path.Base(dir)
	}
	// Release-style name: no spaces, dots or underscores between words.
	release := !strings.Contains(strings.TrimSpace(name), " ") && (strings.Count(name, ".") >= 2 || strings.Contains(name, "_"))
	s := bookBracketsRe.ReplaceAllString(name, " ")
	var b Book
	// Parentheses hold a year, tags, or a part of the title (kept).
	s = bookParensRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimSpace(m[1 : len(m)-1])
		if y := bookYearRe.FindString(inner); y != "" && len(inner) == 4 {
			b.Year, _ = strconv.Atoi(y)
			return " "
		}
		if bookTags[strings.ToLower(inner)] || bookVersionRe.MatchString(inner) {
			return " "
		}
		return " " + inner + " "
	})
	if release {
		s = bookGroupRe.ReplaceAllString(strings.TrimSpace(s), "")
		s = dotsToSpaces(s)
	}
	var words []string
	for _, w := range strings.Fields(s) {
		if !bookTags[strings.ToLower(strings.Trim(w, ",.-"))] && !bookVersionRe.MatchString(w) {
			words = append(words, w)
		}
	}
	s = strings.Join(words, " ")
	// Year: the last one in the name, unless it is the whole title ("1984").
	if years := bookYearRe.FindAllStringIndex(s, -1); len(years) > 0 {
		last := years[len(years)-1]
		if rest := strings.TrimSpace(s[:last[0]] + s[last[1]:]); rest != "" {
			b.Year, _ = strconv.Atoi(s[last[0]:last[1]])
			s = bookSpaceRe.ReplaceAllString(rest, " ")
		}
	}
	for _, re := range bookMarkers {
		m := re.FindStringSubmatchIndex(s)
		if m == nil {
			continue
		}
		n, err := strconv.ParseFloat(strings.ReplaceAll(s[m[2]:m[3]], ",", "."), 64)
		if err != nil {
			continue
		}
		b.Series = cleanBookPart(s[:m[0]])
		if b.Series == "" {
			b.Series = parent
		}
		if b.Series == "" {
			continue // a bare number with no series: that is the title
		}
		b.Number, b.HasNumber = n, true
		return b
	}
	if m := bookTrailingRe.FindStringSubmatch(s); m != nil && parent != "" && Key(m[1]) == Key(parent) {
		if n, err := strconv.ParseFloat(strings.ReplaceAll(m[2], ",", "."), 64); err == nil {
			b.Series, b.Number, b.HasNumber = parent, n, true
			return b
		}
	}
	b.Title = cleanBookPart(s)
	if b.Title == "" {
		b.Title = strings.TrimSpace(name)
	}
	return b
}

// dotsToSpaces replaces the dots and underscores that separate words, keeping those inside a number
// ("T12.5").
func dotsToSpaces(s string) string {
	b := []byte(s)
	digit := func(i int) bool { return i >= 0 && i < len(b) && b[i] >= '0' && b[i] <= '9' }
	for i, c := range b {
		if c == '_' || (c == '.' && (!digit(i-1) || !digit(i+1))) {
			b[i] = ' '
		}
	}
	return string(b)
}

func cleanBookPart(s string) string {
	return strings.TrimSpace(strings.Trim(bookSpaceRe.ReplaceAllString(s, " "), " -_.,:;#("))
}
