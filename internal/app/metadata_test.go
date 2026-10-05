package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// fakeImages serves the images the test NFO files point to: any /img/<name> URL returns a real JPEG
// image, except /img/missing.jpg (404). hits counts requests by name.
func fakeImages(t *testing.T) (srv *httptest.Server, hits func(name string) int) {
	t.Helper()
	jpeg, err := os.ReadFile(filepath.Join(testfixtures.Library(t), "Movies", "Big Test Movie (2020)", "poster.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	counts := map[string]int{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/img/")
		mu.Lock()
		counts[name]++
		mu.Unlock()
		if name == "missing.jpg" || !strings.HasPrefix(r.URL.Path, "/img/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpeg)
	}))
	t.Cleanup(srv.Close)
	return srv, func(name string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[name]
	}
}

func writeText(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func imageSources(v domain.ItemView) map[domain.ImageKind]domain.ImageSource {
	out := map[domain.ImageKind]domain.ImageSource{}
	for _, img := range v.Images {
		out[img.Kind] = img.Source
	}
	return out
}

// rescan scans a library again and waits until everything is processed.
func rescan(t *testing.T, a *App, libID domain.ID) {
	t.Helper()
	mustNil(t, a.ScanLibrary(context.Background(), libID))
	waitIdle(t, a)
}

func TestMovieMetadataFromNFO(t *testing.T) {
	if testing.Short() {
		t.Skip("scan and downloads")
	}
	srv, hits := fakeImages(t)
	a, _ := startMediaApp(t, func(o *Options) {
		o.HTTPClient, o.MetadataDir = srv.Client(), t.TempDir()
	})
	ctx := context.Background()
	_, p := setupAdmin(t, a)
	root := t.TempDir()
	dir := filepath.Join(root, "Big Test Movie (2020)")
	copyTree(t, filepath.Join(testfixtures.Root(), "Movies", "Big Test Movie (2020)"), dir)
	img := func(name string) string { return srv.URL + "/img/" + name }
	nfo := `<movie><title>Big Movie</title><year>2020</year><plot>Overview from the NFO.</plot><mpaa>12</mpaa>
<thumb aspect="banner">` + img("banner-art.jpg") + `</thumb>
<thumb aspect="poster">` + img("poster-art.jpg") + `</thumb>
<actor><name>Audrey Tautou</name><role>Amélie</role><thumb>` + img("audrey.jpg") + `</thumb></actor>
<actor><name>Unknown</name><thumb>` + img("missing.jpg") + `</thumb></actor>
</movie>`
	writeText(t, filepath.Join(dir, "movie.nfo"), nfo)
	lib, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{root}, "")
	mustNil(t, err)
	waitIdle(t, a)

	movie := func() (domain.ItemView, []domain.Credit) {
		t.Helper()
		page, err := a.ListMovies(ctx, p, ListQuery{PageSize: 1})
		mustNil(t, err)
		if len(page.Items) != 1 {
			t.Fatalf("%d movies", len(page.Items))
		}
		v, d, err := a.Movie(ctx, p, page.Items[0].Item.ID)
		mustNil(t, err)
		return v, d.Credits
	}
	v, credits := movie()
	if v.Item.Title != "Big Movie" || v.Item.Overview != "Overview from the NFO." || v.Item.OfficialRating != "12" {
		t.Errorf("movie: %+v", v.Item)
	}
	// Local images first; the banner, missing from the folder, is downloaded.
	src := imageSources(v)
	if src[domain.ImagePoster] != domain.ImageLocal || src[domain.ImageBackdrop] != domain.ImageLocal || src[domain.ImageBanner] != domain.ImageRemote {
		t.Errorf("images: %v", src)
	}
	if hits("poster-art.jpg") != 0 || hits("banner-art.jpg") != 1 || hits("audrey.jpg") != 1 || hits("missing.jpg") != 1 {
		t.Errorf("downloads: poster %d, banner %d, photo %d, missing %d",
			hits("poster-art.jpg"), hits("banner-art.jpg"), hits("audrey.jpg"), hits("missing.jpg"))
	}
	if len(credits) != 2 || credits[0].Image == nil || credits[0].Image.Hash == "" || credits[1].Image != nil {
		t.Errorf("credits: %+v", credits)
	}

	// NFO edited and banner put in the folder: both are read at the next scan. The person keeps
	// their photo even though the NFO now points to another one.
	writeText(t, filepath.Join(dir, "movie.nfo"), strings.NewReplacer(
		"Big Movie", "Big Movie, extended cut", "audrey.jpg", "audrey-2.jpg").Replace(nfo))
	copyTree(t, filepath.Join(dir, "poster.jpg"), filepath.Join(dir, "banner.jpg"))
	rescan(t, a, lib.ID)
	v, credits = movie()
	if v.Item.Title != "Big Movie, extended cut" || imageSources(v)[domain.ImageBanner] != domain.ImageLocal {
		t.Errorf("after the edit: %q %v", v.Item.Title, imageSources(v))
	}
	if len(credits) == 0 {
		t.Fatal("credits lost")
	}
	if hits("audrey-2.jpg") != 0 || hits("banner-art.jpg") != 1 || credits[0].Image == nil {
		t.Errorf("photo downloaded again: %d, banner %d", hits("audrey-2.jpg"), hits("banner-art.jpg"))
	}

	// Purge: the downloaded banner is no longer used, the photo still is.
	// Downloaded files: the whole metadata folder except scrubbing thumbnails.
	downloaded := func() []string {
		var files []string
		_ = filepath.Walk(a.metadataDir, func(p string, info os.FileInfo, err error) error {
			switch {
			case err != nil:
				return err
			case info.IsDir() && info.Name() == "trickplay":
				return filepath.SkipDir
			case !info.IsDir():
				files = append(files, p)
			}
			return nil
		})
		return files
	}
	files := downloaded()
	if len(files) != 2 {
		t.Fatalf("downloaded files: %v", files)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, p := range files {
		mustNil(t, os.Chtimes(p, old, old))
	}
	mustNil(t, a.purgeMetadata(ctx, ""))
	var left []string
	for _, p := range downloaded() {
		left = append(left, filepath.Base(filepath.Dir(filepath.Dir(p)))+"/"+filepath.Base(p))
	}
	if len(left) != 1 || !strings.Contains(left[0], "photo-") {
		t.Errorf("after the purge: %v", left)
	}
}

func TestSeriesMetadataChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("full scan")
	}
	a, _ := startMediaApp(t)
	ctx := context.Background()
	_, p := setupAdmin(t, a)
	root := t.TempDir()
	show := filepath.Join(root, "Café Stories (2022)")
	copyTree(t, filepath.Join(testfixtures.Root(), "Shows", "Café Stories (2022)"), show)
	lib, err := a.CreateLibrary(ctx, "Shows", domain.LibraryShows, []string{root}, "")
	mustNil(t, err)
	waitIdle(t, a)

	series := func() (domain.ItemView, []domain.ItemView, []domain.ItemView) {
		t.Helper()
		page, err := a.ListSeries(ctx, p, ListQuery{PageSize: 1})
		mustNil(t, err)
		if len(page.Items) != 1 {
			t.Fatalf("%d series", len(page.Items))
		}
		v, _, seasons, err := a.Series(ctx, p, page.Items[0].Item.ID)
		mustNil(t, err)
		eps, err := a.Episodes(ctx, p, v.Item.ID, nil)
		mustNil(t, err)
		return v, seasons, eps
	}
	v, seasons, eps := series()
	if v.Item.Title != "Café Stories" || len(seasons) != 2 || len(eps) != 4 || eps[0].Item.Title != "Part 1" {
		t.Fatalf("series: %q, %d seasons, %d episodes", v.Item.Title, len(seasons), len(eps))
	}

	// Sonarr rewrites tvshow.nfo and puts the season 1 poster in the series folder; an episode NFO
	// changes too.
	writeText(t, filepath.Join(show, "tvshow.nfo"), `<tvshow><title>Café Stories, renamed</title><mpaa>TV-14</mpaa></tvshow>`)
	copyTree(t, filepath.Join(show, "poster.jpg"), filepath.Join(show, "season01-poster.jpg"))
	writeText(t, filepath.Join(show, "Season 01", "Café Stories (2022) S01E02.nfo"),
		`<episodedetails><title>The second</title><season>1</season><episode>2</episode><aired>2022-03-08</aired></episodedetails>`)
	rescan(t, a, lib.ID)
	v, seasons, eps = series()
	if v.Item.Title != "Café Stories, renamed" || v.Item.OfficialRating != "TV-14" || v.Item.Overview != "" {
		t.Errorf("series read again: %+v", v.Item)
	}
	if imageSources(seasons[0])[domain.ImagePoster] != domain.ImageLocal || len(seasons[1].Images) != 0 {
		t.Errorf("season posters: %v %v", seasons[0].Images, seasons[1].Images)
	}
	if eps[1].Item.Title != "The second" || eps[1].Item.PremiereDate != "2022-03-08" || eps[0].Item.Title != "Part 1" {
		t.Errorf("episodes: %q (%s), %q", eps[1].Item.Title, eps[1].Item.PremiereDate, eps[0].Item.Title)
	}

	// Series NFO removed: the title goes back to the folder name.
	mustNil(t, os.Remove(filepath.Join(show, "tvshow.nfo")))
	rescan(t, a, lib.ID)
	if v, _, _ = series(); v.Item.Title != "Café Stories" || v.Item.OfficialRating != "" {
		t.Errorf("without an NFO: %+v", v.Item)
	}
}
