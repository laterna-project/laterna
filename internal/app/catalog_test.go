package app

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// catalogApp scans the fixture movies and series and returns the caller (profile picked).
func catalogApp(t *testing.T) (*App, domain.Principal, domain.Library, domain.Library) {
	t.Helper()
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	films, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{filepath.Join(testfixtures.Root(), "Films")}, "")
	mustNil(t, err)
	shows, err := a.CreateLibrary(ctx, "Séries", domain.LibraryShows, []string{filepath.Join(testfixtures.Root(), "Séries")}, "")
	mustNil(t, err)
	waitIdle(t, a)
	return a, p, films, shows
}

func titles(views []domain.ItemView) []string {
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.Item.Title
	}
	return out
}

func TestCatalogMovies(t *testing.T) {
	a, p, films, _ := catalogApp(t)
	ctx := context.Background()

	var all []domain.ItemView
	q := ListQuery{LibraryID: &films.ID, PageSize: 2}
	for {
		page, err := a.ListMovies(ctx, p, q)
		mustNil(t, err)
		if page.Total != 5 {
			t.Fatalf("total: %d", page.Total)
		}
		all = append(all, page.Items...)
		if page.NextPageToken == "" {
			break
		}
		q.PageToken = page.NextPageToken
	}
	if got := titles(all); !slices.IsSorted(got) || len(got) != 5 || got[0] != "Big Test Movie" {
		t.Errorf("movies by title: %v", got)
	}
	// A token only works for the list that produced it.
	q.Sort = domain.SortAdded
	if _, err := a.ListMovies(ctx, p, q); !isKind(err, domain.ErrInvalid) {
		t.Errorf("token from another list: %v", err)
	}
	if _, err := a.ListMovies(ctx, p, ListQuery{PageSize: 500}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("page too big: %v", err)
	}
	if _, err := a.ListMovies(ctx, domain.Principal{Account: p.Account}, ListQuery{}); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("no profile: %v", err)
	}

	big := all[0]
	if len(big.Images) != 2 {
		t.Fatalf("card images: %+v", big.Images)
	}
	view, details, err := a.Movie(ctx, p, big.Item.ID)
	mustNil(t, err)
	if view.Item.Overview == "" || !slices.Equal(details.Genres, []string{"Aventure"}) || len(details.Files) != 1 || details.Files[0].File.Info.Container == "" {
		t.Errorf("details: %+v %+v", view.Item, details)
	}
	if _, _, _, err := a.Series(ctx, p, big.Item.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("a movie requested as a series: %v", err)
	}

	// Played and favorite, then the filters that depend on them.
	mustNil(t, a.SetPlayed(ctx, p, big.Item.ID, true))
	mustNil(t, a.SetFavorite(ctx, p, all[1].Item.ID, true))
	played := true
	if page, _ := a.ListMovies(ctx, p, ListQuery{Played: &played}); len(page.Items) != 1 || page.Items[0].Item.ID != big.Item.ID {
		t.Errorf("played movies: %v", titles(page.Items))
	}
	if page, _ := a.ListMovies(ctx, p, ListQuery{FavoritesOnly: true}); len(page.Items) != 1 || page.Items[0].Item.ID != all[1].Item.ID {
		t.Errorf("favorites: %v", titles(page.Items))
	}
	if page, _ := a.ListMovies(ctx, p, ListQuery{Genre: "aventure"}); len(page.Items) != 1 {
		t.Errorf("genre: %v", titles(page.Items))
	}
	if genres, _ := a.Genres(ctx, p, &films.ID); len(genres) == 0 {
		t.Error("no genre")
	}
}

func TestCatalogSeries(t *testing.T) {
	a, p, _, shows := catalogApp(t)
	ctx := context.Background()

	page, err := a.ListSeries(ctx, p, ListQuery{LibraryID: &shows.ID})
	mustNil(t, err)
	if len(page.Items) != 1 || page.Items[0].EpisodeCount != 4 || page.Items[0].UnplayedCount != 4 {
		t.Fatalf("series: %+v", page.Items)
	}
	played := true
	if _, err := a.ListSeries(ctx, p, ListQuery{Played: &played}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("played filter on series: %v", err)
	}
	series := page.Items[0].Item
	view, details, seasons, err := a.Series(ctx, p, series.ID)
	mustNil(t, err)
	if view.Item.Title != "Série Test" || !slices.Equal(details.Genres, []string{"Drame"}) || len(seasons) != 2 || seasons[0].Season.Number != 1 {
		t.Fatalf("series details: %+v %+v %d seasons", view.Item, details, len(seasons))
	}
	eps, err := a.Episodes(ctx, p, series.ID, nil)
	mustNil(t, err)
	if len(eps) != 4 || eps[0].Episode.Number != 1 || eps[3].Episode.SeasonNumber != 2 {
		t.Fatalf("episodes: %+v", eps)
	}
	if s1, _ := a.Episodes(ctx, p, series.ID, &seasons[0].Item.ID); len(s1) != 3 {
		t.Errorf("episodes of season 1: %d", len(s1))
	}
	other := domain.NewID()
	if _, err := a.Episodes(ctx, p, series.ID, &other); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown season: %v", err)
	}
	if ep, d, err := a.Episode(ctx, p, eps[0].Item.ID); err != nil || ep.SeriesTitle != "Série Test" || len(d.Files) != 1 {
		t.Errorf("episode details: %+v %+v %v", ep, d, err)
	}

	// The whole series played, then season 1 marked unplayed again.
	mustNil(t, a.SetPlayed(ctx, p, series.ID, true))
	if v, _, _, _ := a.Series(ctx, p, series.ID); !v.UserData.Played || v.UnplayedCount != 0 {
		t.Errorf("series played: %+v", v)
	}
	mustNil(t, a.SetPlayed(ctx, p, seasons[0].Item.ID, false))
	if v, _, s, _ := a.Series(ctx, p, series.ID); v.UserData.Played || v.UnplayedCount != 3 || len(s) != 2 || !s[1].UserData.Played {
		t.Errorf("season 1 unplayed again: %+v", v)
	}
}

func TestCatalogSearch(t *testing.T) {
	a, p, _, _ := catalogApp(t)
	ctx := context.Background()
	res, err := a.Search(ctx, p, "serie", 0)
	mustNil(t, err)
	if len(res) == 0 || res[0].Item.Kind != domain.ItemSeries {
		t.Errorf("\"serie\": %v", titles(res))
	}
	if res, _ := a.Search(ctx, p, "BIG tes", 0); !slices.Equal(titles(res), []string{"Big Test Movie"}) {
		t.Errorf("\"BIG tes\": %v", titles(res))
	}
	if _, err := a.Search(ctx, p, "x", 51); !isKind(err, domain.ErrInvalid) {
		t.Errorf("too many results requested: %v", err)
	}
}

func TestCatalogImages(t *testing.T) {
	a, p, films, _ := catalogApp(t)
	ctx := context.Background()
	page, err := a.ListMovies(ctx, p, ListQuery{LibraryID: &films.ID, PageSize: 1})
	mustNil(t, err)
	var poster domain.Image
	for _, img := range page.Items[0].Images {
		if img.Kind == domain.ImagePoster {
			poster = img
		}
	}
	if poster.Hash == "" {
		t.Fatalf("no poster: %+v", page.Items[0].Images)
	}

	orig, err := a.Image(ctx, poster.ID, poster.Hash, 0)
	mustNil(t, err)
	if orig.Path != poster.Path || orig.ContentType != "image/jpeg" {
		t.Errorf("original: %+v", orig)
	}
	small, err := a.Image(ctx, poster.ID, poster.Hash, 150) // rounded up to the 160 tier
	mustNil(t, err)
	f, err := os.Open(small.Path)
	mustNil(t, err)
	cfg, _, err := image.DecodeConfig(f)
	_ = f.Close()
	if err != nil || cfg.Width != 160 || small.ETag == orig.ETag {
		t.Errorf("resized version: %d px %v %+v", cfg.Width, err, small)
	}
	again, err := a.Image(ctx, poster.ID, poster.Hash, 160)
	if err != nil || again.Path != small.Path {
		t.Errorf("cached version: %+v %v", again, err)
	}
	if big, _ := a.Image(ctx, poster.ID, poster.Hash, 5000); big.Path != poster.Path {
		t.Errorf("never upscaled: %+v", big)
	}
	if _, err := a.Image(ctx, poster.ID, "perime", 0); !isKind(err, domain.ErrNotFound) {
		t.Errorf("stale hash: %v", err)
	}
}

// testRoot returns a fixture folder.
func testRoot(name string) string { return filepath.Join(testfixtures.Root(), name) }
