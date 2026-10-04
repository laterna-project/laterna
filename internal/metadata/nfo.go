// Package metadata reads local metadata: NFO files in the Kodi format, as written by Sonarr, Radarr
// or tinyMediaManager, and images sitting next to the media.
package metadata

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
)

// NFO is the useful content of an NFO file (movie, series, season, episode, album or artist).
type NFO struct {
	// Kind is the root element: movie, tvshow, season, episodedetails, album, artist.
	Kind          string
	Title         string
	OriginalTitle string
	SortTitle     string
	Year          int
	// Premiered as YYYY-MM-DD.
	Premiered   string
	Plot        string
	Tagline     string
	MPAA        string
	Rating      float64
	RuntimeMins int
	Genres      []string
	Studios     []string
	// IDs at metadata providers (tmdb, imdb, tvdb; musicbrainz_release, musicbrainz_releasegroup
	// and musicbrainz_artist for music).
	IDs       map[string]string
	Season    int
	Episode   int
	Actors    []Actor
	Directors []string
	Writers   []string
	// Images holds the URL of the image of each kind (<thumb aspect="poster">, <fanart>, episode
	// thumb), to download when there is no local image.
	Images map[domain.ImageKind]string
	// Collection is the franchise the item belongs to (<set>), nil if none.
	Collection *Set
}

// Set is a collection named by an NFO.
type Set struct {
	Name     string
	Overview string
	// TMDbID is the collection's ID at TMDb (the tmdbcolid attribute written by Radarr).
	TMDbID string
}

// Actor is a cast member.
type Actor struct {
	Name  string
	Role  string
	Order int
	// Thumb is the URL of their photo ("" if none).
	Thumb string
}

type rawNFO struct {
	XMLName       xml.Name
	Title         string   `xml:"title"`
	Name          string   `xml:"name"`
	SortName      string   `xml:"sortname"`
	OriginalTitle string   `xml:"originaltitle"`
	SortTitle     string   `xml:"sorttitle"`
	Year          string   `xml:"year"`
	Premiered     string   `xml:"premiered"`
	Aired         string   `xml:"aired"`
	Plot          string   `xml:"plot"`
	Outline       string   `xml:"outline"`
	Review        string   `xml:"review"`
	Biography     string   `xml:"biography"`
	ReleaseDate   string   `xml:"releasedate"`
	Tagline       string   `xml:"tagline"`
	MPAA          string   `xml:"mpaa"`
	Rating        string   `xml:"rating"`
	Runtime       string   `xml:"runtime"`
	Genres        []string `xml:"genre"`
	Studios       []string `xml:"studio"`
	Season        string   `xml:"season"`
	Episode       string   `xml:"episode"`
	Directors     []string `xml:"director"`
	Credits       []string `xml:"credits"`
	TmdbID        string   `xml:"tmdbid"`
	ImdbID        string   `xml:"imdbid"`
	TvdbID        string   `xml:"tvdbid"`
	// Music: album (release, release group) and artist.
	MBRelease      string `xml:"musicbrainzalbumid"`
	MBReleaseGroup string `xml:"musicbrainzreleasegroupid"`
	MBArtist       string `xml:"musicbrainzartistid"`
	UniqueIDs      []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
	Ratings []struct {
		Default string `xml:"default,attr"`
		Value   string `xml:"value"`
	} `xml:"ratings>rating"`
	Actors []struct {
		Name  string `xml:"name"`
		Role  string `xml:"role"`
		Order string `xml:"order"`
		Thumb string `xml:"thumb"`
	} `xml:"actor"`
	Thumbs []rawThumb `xml:"thumb"`
	Set    *struct {
		TMDbID   string `xml:"tmdbcolid,attr"`
		Name     string `xml:"name"`
		Overview string `xml:"overview"`
		Text     string `xml:",chardata"`
	} `xml:"set"`
	Fanart struct {
		URL    string     `xml:"url,attr"`
		Thumbs []rawThumb `xml:"thumb"`
	} `xml:"fanart"`
}

type rawThumb struct {
	Aspect string `xml:"aspect,attr"`
	Type   string `xml:"type,attr"`
	Value  string `xml:",chardata"`
}

var (
	errNotNFO = errors.New("NFO: neither known XML nor a provider link")
	imdbURL   = regexp.MustCompile(`\b(tt\d{7,9})\b`)
	tmdbURL   = regexp.MustCompile(`themoviedb\.org/(?:movie|tv)/(\d+)`)
	tvdbURL   = regexp.MustCompile(`thetvdb\.com/.*?(?:id=|series/)(\d+)`)
)

// ParseNFO reads an NFO. Some only hold a link (IMDb, TMDb), in which case the IDs are taken from
// it.
func ParseNFO(r io.Reader) (NFO, error) {
	data, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err != nil {
		return NFO{}, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // UTF-8 BOM

	var raw rawNFO
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charsetReader
	dec.Strict = false
	if err := dec.Decode(&raw); err != nil || raw.XMLName.Local == "" {
		ids := idsFromLinks(string(data))
		if len(ids) == 0 {
			return NFO{}, errNotNFO
		}
		return NFO{IDs: ids}, nil
	}
	n := NFO{
		Kind:          strings.ToLower(raw.XMLName.Local),
		Title:         text(firstNonEmpty(raw.Title, raw.Name)),
		OriginalTitle: text(raw.OriginalTitle),
		SortTitle:     text(firstNonEmpty(raw.SortTitle, raw.SortName)),
		Year:          atoi(raw.Year),
		Premiered:     date(firstNonEmpty(raw.Premiered, raw.Aired, raw.ReleaseDate)),
		Plot:          text(firstNonEmpty(raw.Plot, raw.Review, raw.Biography, raw.Outline)),
		Tagline:       text(raw.Tagline),
		MPAA:          text(raw.MPAA),
		RuntimeMins:   atoi(raw.Runtime),
		Genres:        splitList(raw.Genres),
		Studios:       splitList(raw.Studios),
		Season:        atoi(raw.Season),
		Episode:       atoi(raw.Episode),
		Directors:     splitList(raw.Directors),
		Writers:       splitList(raw.Credits),
		IDs:           map[string]string{},
	}
	if n.Year == 0 && len(n.Premiered) >= 4 {
		n.Year = atoi(n.Premiered[:4])
	}
	n.Rating = rating(raw)
	for _, u := range raw.UniqueIDs {
		if t, v := strings.ToLower(strings.TrimSpace(u.Type)), strings.TrimSpace(u.Value); t != "" && v != "" {
			n.IDs[t] = v
		}
	}
	for key, v := range map[string]string{
		"tmdb": raw.TmdbID, "imdb": raw.ImdbID, "tvdb": raw.TvdbID, "musicbrainz_release": raw.MBRelease,
		"musicbrainz_releasegroup": raw.MBReleaseGroup, "musicbrainz_artist": raw.MBArtist,
	} {
		if v = strings.TrimSpace(v); v != "" && n.IDs[key] == "" {
			n.IDs[key] = v
		}
	}
	for k, v := range idsFromLinks(string(data)) {
		if n.IDs[k] == "" {
			n.IDs[k] = v
		}
	}
	if len(n.IDs) == 0 {
		n.IDs = nil
	}
	for i, a := range raw.Actors {
		if name := text(a.Name); name != "" {
			order := i
			if o, err := strconv.Atoi(strings.TrimSpace(a.Order)); err == nil {
				order = o
			}
			n.Actors = append(n.Actors, Actor{Name: name, Role: text(a.Role), Order: order, Thumb: webURL("", a.Thumb)})
		}
	}
	n.Images = images(n.Kind, raw)
	if s := raw.Set; s != nil {
		// Two forms: <set><name>...</name></set> (Kodi 17+, Radarr) and <set>...</set>.
		name := text(s.Name)
		if name == "" {
			name = text(s.Text)
		}
		if name != "" {
			n.Collection = &Set{Name: name, Overview: text(s.Overview), TMDbID: strings.TrimSpace(s.TMDbID)}
		}
	}
	return n, nil
}

// aspects maps the aspect attribute of an image to a kind.
var aspects = map[string]domain.ImageKind{
	"poster": domain.ImagePoster, "banner": domain.ImageBanner, "clearlogo": domain.ImageLogo, "logo": domain.ImageLogo,
	"landscape": domain.ImageThumb, "thumb": domain.ImageThumb, "fanart": domain.ImageBackdrop,
}

// images picks the first URL of each image kind. Without an aspect the image is the poster (the
// thumb for an episode). Season images in a tvshow.nfo (type="season") and unknown kinds (clearart,
// discart...) are ignored.
func images(kind string, raw rawNFO) map[domain.ImageKind]string {
	out := map[domain.ImageKind]string{}
	add := func(k domain.ImageKind, u string) {
		if _, ok := out[k]; !ok && u != "" {
			out[k] = u
		}
	}
	for _, t := range raw.Thumbs {
		if t.Type != "" && !strings.EqualFold(t.Type, "show") {
			continue
		}
		aspect := strings.ToLower(strings.TrimSpace(t.Aspect))
		k, ok := aspects[aspect]
		if aspect == "" {
			k, ok = domain.ImagePoster, true
			if kind == "episodedetails" {
				k = domain.ImageThumb
			}
		}
		if ok {
			add(k, webURL("", t.Value))
		}
	}
	for _, t := range raw.Fanart.Thumbs {
		add(domain.ImageBackdrop, webURL(raw.Fanart.URL, t.Value))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// webURL returns a full HTTP(S) URL, or "" for anything else (local path, smb://). base is
// prepended to a relative URL (the old <fanart url="..."> format).
func webURL(base, s string) string {
	s = strings.TrimSpace(s)
	if base = strings.TrimSpace(base); base != "" && s != "" && !strings.Contains(s, "://") {
		s = strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(s, "/")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return s
}

func rating(raw rawNFO) float64 {
	for _, r := range raw.Ratings {
		if r.Default == "true" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(r.Value), 64); err == nil {
				return f
			}
		}
	}
	if len(raw.Ratings) > 0 {
		if f, err := strconv.ParseFloat(strings.TrimSpace(raw.Ratings[0].Value), 64); err == nil {
			return f
		}
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(raw.Rating), 64)
	return f
}

func idsFromLinks(s string) map[string]string {
	ids := map[string]string{}
	if m := imdbURL.FindStringSubmatch(s); m != nil {
		ids["imdb"] = m[1]
	}
	if m := tmdbURL.FindStringSubmatch(s); m != nil {
		ids["tmdb"] = m[1]
	}
	if m := tvdbURL.FindStringSubmatch(s); m != nil {
		ids["tvdb"] = m[1]
	}
	return ids
}

// charsetReader accepts NFO files declared as Latin-1 / Windows-1252 (common with older tools) on
// top of UTF-8.
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "utf-8", "utf8", "":
		return input, nil
	case "iso-8859-1", "iso-8859-15", "latin1", "windows-1252", "cp1252":
		data, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		if utf8.Valid(data) {
			// Declared Latin-1 but written in UTF-8: take it as it is.
			return bytes.NewReader(data), nil
		}
		out := make([]rune, len(data))
		for i, b := range data {
			out[i] = rune(b)
		}
		return strings.NewReader(string(out)), nil
	default:
		return nil, fmt.Errorf("unsupported encoding %q", charset)
	}
}

// splitList splits lists written as "Action / Adventure", on top of repeated elements.
func splitList(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		for part := range strings.SplitSeq(v, "/") {
			p := text(part)
			if p != "" && !seen[strings.ToLower(p)] {
				seen[strings.ToLower(p)] = true
				out = append(out, p)
			}
		}
	}
	return out
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

func date(s string) string {
	return dateRe.FindString(strings.TrimSpace(s))
}

func text(s string) string { return strings.TrimSpace(strings.ToValidUTF8(s, "�")) }

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
