package metadata

import (
	"encoding/xml"
	"html"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Books: metadata of an EPUB (its OPF, or the "metadata.opf" Calibre writes next to the file), of a
// comic (ComicInfo.xml) and of a PDF (Info dictionary).

// BookMeta is what one source says about a book. An empty field says nothing.
type BookMeta struct {
	Title     string
	SortTitle string
	// Series and Number of the volume. HasNumber tells volume 0 from an unknown volume.
	Series    string
	Number    float64
	HasNumber bool
	Authors   []string
	// Illustrators are the artists of a comic.
	Illustrators []string
	Description  string
	Publisher    string
	// Date as YYYY-MM-DD or YYYY.
	Date     string
	Language string
	ISBN     string
	Genres   []string
	// RightToLeft means pages go right to left (manga).
	RightToLeft bool
}

// Year returns the year of the date, 0 if unknown.
func (m BookMeta) Year() int {
	if len(m.Date) >= 4 {
		y, _ := strconv.Atoi(m.Date[:4])
		return y
	}
	return 0
}

// Merge lays over on top of m: whatever over sets wins.
func (m BookMeta) Merge(over BookMeta) BookMeta {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&m.Title, over.Title)
	set(&m.SortTitle, over.SortTitle)
	if over.Series != "" {
		m.Series, m.Number, m.HasNumber = over.Series, over.Number, over.HasNumber
	} else if over.HasNumber {
		m.Number, m.HasNumber = over.Number, true
	}
	if len(over.Authors) > 0 {
		m.Authors = over.Authors
	}
	if len(over.Illustrators) > 0 {
		m.Illustrators = over.Illustrators
	}
	set(&m.Description, over.Description)
	set(&m.Publisher, over.Publisher)
	set(&m.Date, over.Date)
	set(&m.Language, over.Language)
	set(&m.ISBN, over.ISBN)
	if len(over.Genres) > 0 {
		m.Genres = over.Genres
	}
	m.RightToLeft = m.RightToLeft || over.RightToLeft
	return m
}

// OPF is what we keep from an OPF file: the metadata and, for an EPUB, its cover.
type OPF struct {
	Meta BookMeta
	// CoverHref is the path of the cover image relative to the OPF; empty if there is none.
	CoverHref string
}

type rawOPF struct {
	Metadata struct {
		Titles []struct {
			ID    string `xml:"id,attr"`
			Value string `xml:",chardata"`
		} `xml:"title"`
		Creators     []opfPerson `xml:"creator"`
		Contributors []opfPerson `xml:"contributor"`
		Descriptions []string    `xml:"description"`
		Publishers   []string    `xml:"publisher"`
		Dates        []string    `xml:"date"`
		Languages    []string    `xml:"language"`
		Identifiers  []struct {
			Scheme string `xml:"scheme,attr"`
			Value  string `xml:",chardata"`
		} `xml:"identifier"`
		Subjects []string `xml:"subject"`
		Metas    []struct {
			ID       string `xml:"id,attr"`
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
			Refines  string `xml:"refines,attr"`
			Value    string `xml:",chardata"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		Direction string `xml:"page-progression-direction,attr"`
	} `xml:"spine"`
	Guide struct {
		References []struct {
			Type string `xml:"type,attr"`
			Href string `xml:"href,attr"`
		} `xml:"reference"`
	} `xml:"guide"`
}

type opfPerson struct {
	ID    string `xml:"id,attr"`
	Role  string `xml:"role,attr"`
	Value string `xml:",chardata"`
}

// ParseOPF reads an OPF file (EPUB 2 or 3, Calibre).
func ParseOPF(r io.Reader) (OPF, error) {
	var raw rawOPF
	dec := xml.NewDecoder(r)
	dec.CharsetReader = charsetReader
	dec.Strict = false
	if err := dec.Decode(&raw); err != nil {
		return OPF{}, err
	}
	md := raw.Metadata
	// EPUB 3: properties attached to an element (refines="#id").
	refines := map[string]map[string]string{}
	var m BookMeta
	for _, meta := range md.Metas {
		value := text(firstNonEmpty(meta.Content, meta.Value))
		switch {
		case meta.Refines != "":
			id := strings.TrimPrefix(meta.Refines, "#")
			if refines[id] == nil {
				refines[id] = map[string]string{}
			}
			refines[id][meta.Property] = value
		case meta.Name == "calibre:series":
			m.Series = value
		case meta.Name == "calibre:series_index":
			m.Number, m.HasNumber = parseNumber(value)
		case meta.Name == "calibre:title_sort":
			m.SortTitle = value
		}
	}
	// EPUB 3 series: "belongs-to-collection" of type series (or without a type).
	for _, meta := range md.Metas {
		if meta.Property != "belongs-to-collection" || m.Series != "" {
			continue
		}
		ref := refines[meta.ID]
		if t := ref["collection-type"]; t != "" && t != "series" {
			continue
		}
		m.Series = text(meta.Value)
		if n, ok := parseNumber(ref["group-position"]); ok {
			m.Number, m.HasNumber = n, true
		}
	}
	// Title: the one marked "main", otherwise the first.
	for _, t := range md.Titles {
		if v := text(t.Value); v != "" && (m.Title == "" || refines[t.ID]["title-type"] == "main") {
			m.Title = v
		}
	}
	for _, p := range append(md.Creators, md.Contributors...) {
		name := text(p.Value)
		role := strings.ToLower(firstNonEmpty(p.Role, refines[p.ID]["role"]))
		switch {
		case name == "":
		case role == "aut" || (role == "" && contains(md.Creators, p)):
			m.Authors = appendUnique(m.Authors, name)
		case role == "ill" || role == "art":
			m.Illustrators = appendUnique(m.Illustrators, name)
		}
	}
	if len(md.Descriptions) > 0 {
		m.Description = plainText(md.Descriptions[0])
	}
	if len(md.Publishers) > 0 {
		m.Publisher = text(md.Publishers[0])
	}
	if len(md.Dates) > 0 {
		m.Date = bookDate(md.Dates[0])
	}
	if len(md.Languages) > 0 {
		m.Language = text(md.Languages[0])
	}
	for _, id := range md.Identifiers {
		if isbn := isbnOf(id.Scheme, id.Value); isbn != "" {
			m.ISBN = isbn
			break
		}
	}
	for _, s := range md.Subjects {
		m.Genres = appendUnique(m.Genres, text(s))
	}
	m.RightToLeft = raw.Spine.Direction == "rtl"

	out := OPF{Meta: m}
	// Cover: EPUB 3 ("cover-image"), then EPUB 2 (<meta name="cover">), then the guide.
	var coverID string
	for _, meta := range md.Metas {
		if meta.Name == "cover" {
			coverID = meta.Content
		}
	}
	for _, it := range raw.Manifest.Items {
		if strings.Contains(" "+it.Properties+" ", " cover-image ") {
			out.CoverHref = it.Href
			break
		}
		if coverID != "" && it.ID == coverID && strings.HasPrefix(it.MediaType, "image/") {
			out.CoverHref = it.Href
		}
	}
	if out.CoverHref == "" {
		for _, ref := range raw.Guide.References {
			if strings.EqualFold(ref.Type, "cover") && isImagePath(ref.Href) {
				out.CoverHref = ref.Href
			}
		}
	}
	return out, nil
}

func contains(list []opfPerson, p opfPerson) bool {
	for _, q := range list {
		if q == p {
			return true
		}
	}
	return false
}

// ComicInfo is what we keep from a ComicInfo.xml file (comics, manga).
type ComicInfo struct {
	Meta BookMeta
	// FrontCover is the index of the cover among the pages; -1 if not given.
	FrontCover int
}

type rawComicInfo struct {
	Title       string `xml:"Title"`
	Series      string `xml:"Series"`
	Number      string `xml:"Number"`
	Volume      string `xml:"Volume"`
	Summary     string `xml:"Summary"`
	Year        int    `xml:"Year"`
	Month       int    `xml:"Month"`
	Day         int    `xml:"Day"`
	Writer      string `xml:"Writer"`
	Penciller   string `xml:"Penciller"`
	Inker       string `xml:"Inker"`
	Publisher   string `xml:"Publisher"`
	Genre       string `xml:"Genre"`
	LanguageISO string `xml:"LanguageISO"`
	Manga       string `xml:"Manga"`
	Pages       struct {
		Pages []struct {
			Image string `xml:"Image,attr"`
			Type  string `xml:"Type,attr"`
		} `xml:"Page"`
	} `xml:"Pages"`
}

// ParseComicInfo reads a ComicInfo.xml file.
func ParseComicInfo(r io.Reader) (ComicInfo, error) {
	var raw rawComicInfo
	dec := xml.NewDecoder(r)
	dec.CharsetReader = charsetReader
	dec.Strict = false
	if err := dec.Decode(&raw); err != nil {
		return ComicInfo{}, err
	}
	m := BookMeta{
		Title: text(raw.Title), Series: text(raw.Series), Description: plainText(raw.Summary),
		Publisher: text(raw.Publisher), Language: text(raw.LanguageISO),
		Authors: splitNames(raw.Writer), Illustrators: splitNames(raw.Penciller),
		RightToLeft: strings.EqualFold(strings.TrimSpace(raw.Manga), "YesAndRightToLeft"),
	}
	if len(m.Illustrators) == 0 {
		m.Illustrators = splitNames(raw.Inker)
	}
	if n, ok := parseNumber(raw.Number); ok {
		m.Number, m.HasNumber = n, true
	} else if n, ok := parseNumber(raw.Volume); ok {
		m.Number, m.HasNumber = n, true
	}
	for g := range strings.SplitSeq(raw.Genre, ",") {
		if g = text(g); g != "" {
			m.Genres = appendUnique(m.Genres, g)
		}
	}
	switch {
	case raw.Year > 0 && raw.Month > 0 && raw.Day > 0:
		m.Date = time.Date(raw.Year, time.Month(raw.Month), raw.Day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
	case raw.Year > 0:
		m.Date = strconv.Itoa(raw.Year)
	}
	out := ComicInfo{Meta: m, FrontCover: -1}
	for _, p := range raw.Pages.Pages {
		if strings.EqualFold(p.Type, "FrontCover") {
			if n, err := strconv.Atoi(strings.TrimSpace(p.Image)); err == nil && n >= 0 {
				out.FrontCover = n
				break
			}
		}
	}
	return out, nil
}

// PDFMeta converts the Info dictionary of a PDF (Title, Author, Subject, Keywords, CreationDate).
func PDFMeta(info map[string]string) BookMeta {
	m := BookMeta{Title: text(info["Title"]), Authors: splitNames(info["Author"])}
	if d := info["CreationDate"]; len(d) >= 6 && strings.HasPrefix(d, "D:") {
		m.Date = d[2:6]
		if len(d) >= 10 {
			if t, err := time.Parse("20060102", d[2:10]); err == nil {
				m.Date = t.Format(time.DateOnly)
			}
		}
	}
	// Some tools write the file name or "untitled" as the title.
	lower := strings.ToLower(m.Title)
	if strings.HasSuffix(lower, ".pdf") || strings.HasSuffix(lower, ".doc") || strings.HasSuffix(lower, ".docx") || lower == "untitled" {
		m.Title = ""
	}
	return m
}

// BookArtwork finds the cover of a book: "<file>.jpg" next to it, or "cover.jpg" if it is alone in
// its folder (Calibre library).
func BookArtwork(dir, base string, alone bool) ([]Artwork, error) {
	names := candidates{domain.ImagePoster: {"{base}", "{base}-cover"}}
	if alone {
		names[domain.ImagePoster] = append(names[domain.ImagePoster], "cover", "folder")
	}
	return find(dir, base, names)
}

var (
	numberRe = regexp.MustCompile(`^\d+(?:[.,]\d+)?$`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]*>`)
	spacesRe = regexp.MustCompile(`[ \t]+`)
	blankRe  = regexp.MustCompile(`\n{3,}`)
)

// parseNumber reads a volume number ("12", "12.5", "12,5").
func parseNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if !numberRe.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	return n, err == nil
}

// plainText turns an HTML description (common in OPF files) into text, with paragraphs separated by
// a blank line.
func plainText(s string) string {
	for _, br := range []string{"<br>", "<br/>", "<br />", "</p>", "</div>", "</li>"} {
		s = strings.ReplaceAll(s, br, "\n\n")
		s = strings.ReplaceAll(s, strings.ToUpper(br), "\n\n")
	}
	s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spacesRe.ReplaceAllString(l, " "))
	}
	return text(blankRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// bookDate turns an OPF date into YYYY-MM-DD (or YYYY). Calibre writes local midnight in UTC
// ("2023-11-30T23:00:00+00:00" for December 1 in Paris), so a date with a time is rounded to the
// nearest day.
func bookDate(s string) string {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Add(12 * time.Hour).Format(time.DateOnly)
	}
	if d := date(s); d != "" {
		return d
	}
	if len(s) >= 4 {
		if _, err := strconv.Atoi(s[:4]); err == nil && (len(s) == 4 || s[4] == '-') {
			return s[:4]
		}
	}
	return ""
}

// isbnOf picks an ISBN out of the identifiers of an OPF.
func isbnOf(scheme, value string) string {
	v := strings.TrimSpace(value)
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "urn:isbn:"):
		v = v[len("urn:isbn:"):]
	case strings.HasPrefix(lower, "isbn:"):
		v = v[len("isbn:"):]
	case !strings.EqualFold(scheme, "isbn"):
		return ""
	}
	digits := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == 'X' || r == 'x' {
			return r
		}
		return -1
	}, v)
	if len(digits) != 10 && len(digits) != 13 {
		return ""
	}
	return strings.ToUpper(digits)
}

// splitNames splits "Goscinny, Uderzo" or "Goscinny & Uderzo".
func splitNames(s string) []string {
	var out []string
	for part := range strings.SplitSeq(strings.ReplaceAll(s, "&", ","), ",") {
		if p := text(part); p != "" {
			out = appendUnique(out, p)
		}
	}
	return out
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return list
		}
	}
	return append(list, v)
}

func isImagePath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return true
	}
	return false
}
