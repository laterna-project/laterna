package metadata

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
)

const movieNFO = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>Le Fabuleux Destin d'Amélie Poulain</title>
  <originaltitle>Le Fabuleux Destin d'Amélie Poulain</originaltitle>
  <sorttitle>Fabuleux Destin</sorttitle>
  <ratings>
    <rating name="imdb" max="10"><value>7.8</value></rating>
    <rating name="themoviedb" max="10" default="true"><value>7.9</value></rating>
  </ratings>
  <plot>Amélie, une jeune serveuse…</plot>
  <tagline>Elle va changer votre vie.</tagline>
  <runtime>122</runtime>
  <mpaa>Tous publics</mpaa>
  <uniqueid type="imdb">tt0211915</uniqueid>
  <uniqueid type="tmdb" default="true">194</uniqueid>
  <genre>Comédie / Romance</genre>
  <genre>Comédie</genre>
  <studio>UGC</studio>
  <director>Jean-Pierre Jeunet</director>
  <credits>Guillaume Laurant</credits>
  <premiered>2001-04-25</premiered>
  <thumb aspect="poster" preview="https://image.tmdb.org/t/p/w185/affiche.jpg">https://image.tmdb.org/t/p/original/affiche.jpg</thumb>
  <thumb aspect="poster">https://image.tmdb.org/t/p/original/autre.jpg</thumb>
  <thumb aspect="clearlogo">https://assets.fanart.tv/logo.png</thumb>
  <thumb aspect="discart">https://assets.fanart.tv/disque.png</thumb>
  <thumb aspect="banner">banner.jpg</thumb>
  <fanart url="https://image.tmdb.org/t/p/original"><thumb>/fond.jpg</thumb></fanart>
  <actor><name>Audrey Tautou</name><role>Amélie</role><order>0</order><thumb>https://image.tmdb.org/t/p/original/audrey.jpg</thumb></actor>
  <actor><name>Mathieu Kassovitz</name><role>Nino</role><order>1</order><thumb>smb://nas/photos/mathieu.jpg</thumb></actor>
</movie>
https://www.themoviedb.org/movie/194`

func TestParseMovieNFO(t *testing.T) {
	n, err := ParseNFO(strings.NewReader("\xef\xbb\xbf" + movieNFO))
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != "movie" || n.Title != "Le Fabuleux Destin d'Amélie Poulain" || n.SortTitle != "Fabuleux Destin" {
		t.Errorf("titles: %+v", n)
	}
	if n.Year != 2001 || n.Premiered != "2001-04-25" || n.RuntimeMins != 122 || n.Rating != 7.9 || n.MPAA != "Tous publics" {
		t.Errorf("values: %+v", n)
	}
	if !slices.Equal(n.Genres, []string{"Comédie", "Romance"}) {
		t.Errorf("genres (\"/\" list and duplicates): %v", n.Genres)
	}
	if n.IDs["imdb"] != "tt0211915" || n.IDs["tmdb"] != "194" {
		t.Errorf("IDs: %v", n.IDs)
	}
	if len(n.Actors) != 2 || n.Actors[0].Name != "Audrey Tautou" || n.Actors[1].Role != "Nino" || n.Actors[1].Order != 1 {
		t.Errorf("actors: %+v", n.Actors)
	}
	// Photos: only web addresses count.
	if n.Actors[0].Thumb != "https://image.tmdb.org/t/p/original/audrey.jpg" || n.Actors[1].Thumb != "" {
		t.Errorf("photos: %+v", n.Actors)
	}
	// Images: the first of each known kind. A relative backdrop gets the base URL prepended.
	want := map[domain.ImageKind]string{
		domain.ImagePoster:   "https://image.tmdb.org/t/p/original/affiche.jpg",
		domain.ImageLogo:     "https://assets.fanart.tv/logo.png",
		domain.ImageBackdrop: "https://image.tmdb.org/t/p/original/fond.jpg",
	}
	if !maps.Equal(n.Images, want) {
		t.Errorf("images: %v", n.Images)
	}
	if !slices.Equal(n.Directors, []string{"Jean-Pierre Jeunet"}) || !slices.Equal(n.Writers, []string{"Guillaume Laurant"}) {
		t.Errorf("crew: %v %v", n.Directors, n.Writers)
	}
}

func TestParseEpisodeAndShowNFO(t *testing.T) {
	// As Sonarr writes it: air date, thumb without an aspect.
	ep, err := ParseNFO(strings.NewReader(`<episodedetails><title>Épisode 2</title><season>1</season><episode>2</episode><aired>2022-03-01</aired>
<thumb>https://artworks.thetvdb.com/banners/v4/episode/1/screencap/a.jpg</thumb></episodedetails>`))
	if err != nil || ep.Kind != "episodedetails" || ep.Season != 1 || ep.Episode != 2 || ep.Premiered != "2022-03-01" || ep.Year != 2022 {
		t.Errorf("episode: %+v %v", ep, err)
	}
	if ep.Images[domain.ImageThumb] != "https://artworks.thetvdb.com/banners/v4/episode/1/screencap/a.jpg" || len(ep.Images) != 1 {
		t.Errorf("episode thumb: %v", ep.Images)
	}
	show, err := ParseNFO(strings.NewReader(`<tvshow><title>Série</title><tvdbid>81189</tvdbid><rating>8.5</rating><mpaa>TV-PG</mpaa>
<thumb aspect="poster" type="season" season="1">https://x.test/saison1.jpg</thumb>
<actor><name>Yusuke Kobayashi</name><role>Senkuu Ishigami </role><thumb>https://artworks.thetvdb.com/banners/person/459168/a.jpg</thumb></actor></tvshow>`))
	if err != nil || show.Kind != "tvshow" || show.IDs["tvdb"] != "81189" || show.Rating != 8.5 || show.MPAA != "TV-PG" {
		t.Errorf("series: %+v %v", show, err)
	}
	if show.Images != nil || len(show.Actors) != 1 || show.Actors[0].Role != "Senkuu Ishigami" || show.Actors[0].Thumb == "" {
		t.Errorf("series: season poster should be ignored, actor: %v %+v", show.Images, show.Actors)
	}
}

func TestParseLinkOnlyAndLatin1NFO(t *testing.T) {
	n, err := ParseNFO(strings.NewReader("https://www.imdb.com/title/tt0133093/\n"))
	if err != nil || n.IDs["imdb"] != "tt0133093" {
		t.Errorf("NFO that is just a link: %+v %v", n, err)
	}
	latin1 := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><movie><title>L'\xc9t\xe9 meurtrier</title></movie>"
	n, err = ParseNFO(strings.NewReader(latin1))
	if err != nil || n.Title != "L'Été meurtrier" {
		t.Errorf("Latin-1 NFO: %q %v", n.Title, err)
	}
	if _, err := ParseNFO(strings.NewReader("rien d'utile")); err == nil {
		t.Error("random content accepted")
	}
}

func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func kinds(arts []Artwork) map[domain.ImageKind]string {
	m := map[domain.ImageKind]string{}
	for _, a := range arts {
		m[a.Kind] = filepath.Base(a.Path)
	}
	return m
}

func TestMovieArtwork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Film (2020)")
	touch(t, dir, "Film (2020).mkv", "Folder.JPG", "Film (2020)-fanart.png", "fanart.jpg", "clearlogo.png", "notes.txt")
	arts, err := MovieArtwork(dir, "Film (2020)", true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[domain.ImageKind]string{
		domain.ImagePoster:   "Folder.JPG",
		domain.ImageBackdrop: "Film (2020)-fanart.png", // the file's own name comes before the generic one
		domain.ImageLogo:     "clearlogo.png",
	}
	if got := kinds(arts); !mapsEqual(got, want) {
		t.Errorf("images: %v, want %v", got, want)
	}

	// Movie at the root: the generic images at the root are not its own.
	root := t.TempDir()
	touch(t, root, "A.mkv", "poster.jpg", "A-poster.jpg", "B-poster.jpg")
	arts, _ = MovieArtwork(root, "A", false)
	if got := kinds(arts); !mapsEqual(got, map[domain.ImageKind]string{domain.ImagePoster: "A-poster.jpg"}) {
		t.Errorf("movie at the root: %v", got)
	}
}

func TestSeriesSeasonEpisodeArtwork(t *testing.T) {
	series := filepath.Join(t.TempDir(), "Série")
	season := filepath.Join(series, "Saison 01")
	touch(t, series, "poster.jpg", "fanart.jpg", "season01-poster.jpg", "season-specials-poster.jpg")
	touch(t, season, "folder.jpg", "fanart.jpg", "Série S01E01.mkv", "Série S01E01-thumb.jpg")

	arts, _ := SeriesArtwork(series)
	if got := kinds(arts); got[domain.ImagePoster] != "poster.jpg" || got[domain.ImageBackdrop] != "fanart.jpg" {
		t.Errorf("series: %v", got)
	}
	arts, _ = SeasonArtwork(series, season, 1)
	if got := kinds(arts); got[domain.ImagePoster] != "season01-poster.jpg" || got[domain.ImageBackdrop] != "fanart.jpg" {
		t.Errorf("season 1 (the series folder wins): %v", got)
	}
	arts, _ = SeasonArtwork(series, "", 0)
	if got := kinds(arts); got[domain.ImagePoster] != "season-specials-poster.jpg" {
		t.Errorf("specials: %v", got)
	}
	arts, _ = EpisodeArtwork(season, "Série S01E01")
	if got := kinds(arts); got[domain.ImageThumb] != "Série S01E01-thumb.jpg" {
		t.Errorf("episode: %v", got)
	}
	if _, err := SeriesArtwork(filepath.Join(series, "absent")); err == nil {
		t.Error("missing folder: want an error")
	}
}

func TestNFOPath(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "movie.nfo")
	if got := NFOPath(filepath.Join(dir, "Film.nfo"), filepath.Join(dir, "movie.nfo")); filepath.Base(got) != "movie.nfo" {
		t.Errorf("NFOPath: %q", got)
	}
	if got := NFOPath(filepath.Join(dir, "absent.nfo")); got != "" {
		t.Errorf("NFOPath for a missing file: %q", got)
	}
}

func mapsEqual(a, b map[domain.ImageKind]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestParseSet(t *testing.T) {
	// As Radarr writes it.
	n, err := ParseNFO(strings.NewReader(`<movie><title>Jujutsu Kaisen 0</title><set tmdbcolid="1529614">
    <name>Jujutsu Kaisen Collection</name>
    <overview />
  </set></movie>`))
	if err != nil || n.Collection == nil || *n.Collection != (Set{Name: "Jujutsu Kaisen Collection", TMDbID: "1529614"}) {
		t.Errorf("Radarr: %+v %v", n.Collection, err)
	}
	// Old Kodi form.
	n, err = ParseNFO(strings.NewReader(`<movie><title>Alien</title><set>Alien - La saga</set></movie>`))
	if err != nil || n.Collection == nil || n.Collection.Name != "Alien - La saga" || n.Collection.TMDbID != "" {
		t.Errorf("old form: %+v %v", n.Collection, err)
	}
	if n, _ := ParseNFO(strings.NewReader(`<movie><title>Seul</title><set><name> </name></set></movie>`)); n.Collection != nil {
		t.Errorf("unnamed collection: %+v", n.Collection)
	}
}
