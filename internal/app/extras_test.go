package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
)

// copyFixture copies a fixture file to a library being built.
func copyFixture(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(testRoot(from))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	mustNil(t, os.MkdirAll(filepath.Dir(to), 0o750))
	dst, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	mustNil(t, dst.Close())
}

// Extras are filed under their movie or series, from a folder of extras or a suffix, whatever the
// order the files arrive in. They are listed with it and play from the start, but appear in no list,
// search or history, and are as hidden as their owner.
func TestExtras(t *testing.T) {
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	root := t.TempDir()
	movies, shows := filepath.Join(root, "Movies"), filepath.Join(root, "Shows")
	movie := filepath.Join(movies, "Big Test Movie (2020)")
	show := filepath.Join(shows, "Café Stories (2022)")

	// The extras come first: they wait for their movie and series.
	copyFixture(t, "Movies/Versions (2017)/Versions (2017) - 720p.mp4", filepath.Join(movie, "Trailers", "Teaser.mp4"))
	copyFixture(t, "Movies/Dual Audio (2019)/Dual Audio (2019).mkv", filepath.Join(movie, "Big Test Movie (2020)-featurette.mkv"))
	copyFixture(t, "Shows/Café Stories (2022)/Season 01/Café Stories (2022) S01E02.mkv", filepath.Join(show, "Featurettes", "Behind the counter.mkv"))
	// A release group that looks like a suffix: a movie of its own, its folder has no other one.
	copyFixture(t, "Movies/Stream Dump (2018)/Stream Dump (2018).mkv", filepath.Join(movies, "Lost (2001)", "Lost.2001.BluRay-SCENE.mkv"))
	// A trailer at the root, before its movie: taken for a movie until the movie comes.
	copyFixture(t, "Movies/Versions (2017)/Versions (2017) - 1080p.mp4", filepath.Join(movies, "Second Movie (2021)-trailer.mp4"))
	movieLib, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{movies})
	mustNil(t, err)
	showLib, err := a.CreateLibrary(ctx, "Shows", domain.LibraryShows, []string{shows})
	mustNil(t, err)
	waitIdle(t, a)
	page, err := a.ListMovies(ctx, p, ListQuery{PageSize: 10})
	mustNil(t, err)
	if len(page.Items) != 2 {
		t.Fatalf("movies before their files: %+v", page.Items)
	}

	// Then the movie and an episode: their extras are filed.
	copyFixture(t, "Movies/Big Test Movie (2020)/Big Test Movie (2020).mp4", filepath.Join(movie, "Big Test Movie (2020).mp4"))
	copyFixture(t, "Movies/HDR Test (2021)/HDR Test (2021).mkv", filepath.Join(movies, "Second Movie (2021).mkv"))
	copyFixture(t, "Shows/Café Stories (2022)/Season 01/Café Stories (2022) S01E01.mkv", filepath.Join(show, "Season 01", "Café Stories (2022) S01E01.mkv"))
	mustNil(t, a.ScanLibrary(ctx, movieLib.ID))
	mustNil(t, a.ScanLibrary(ctx, showLib.ID))
	waitIdle(t, a)

	page, err = a.ListMovies(ctx, p, ListQuery{PageSize: 10})
	mustNil(t, err)
	if len(page.Items) != 3 {
		t.Fatalf("movies: %+v", page.Items)
	}
	second := itemByKey(t, a, movieLib, "movie:/secondmovie-2021")
	if trailers, err := a.Extras(ctx, p, second.ID); err != nil || len(trailers) != 1 || trailers[0].Extra.Kind != domain.ExtraTrailer {
		t.Errorf("trailer taken for a movie, then filed: %+v %v", trailers, err)
	}
	big := itemByKey(t, a, movieLib, "movie:Big Test Movie (2020)/bigtestmovie")
	view, details, err := a.Movie(ctx, p, big.ID)
	mustNil(t, err)
	extras, err := a.Extras(ctx, p, big.ID)
	mustNil(t, err)
	if len(extras) != 2 || details.Extras != 2 || details.Trailer == nil || *details.Trailer != extras[0].Item.ID {
		t.Fatalf("extras of %s: %d, trailer %v, %+v", view.Item.Title, details.Extras, details.Trailer, extras)
	}
	teaser, featurette := extras[0], extras[1]
	if teaser.Item.Title != "Teaser" || teaser.Extra == nil || teaser.Extra.Kind != domain.ExtraTrailer ||
		featurette.Item.Title != "Big Test Movie (2020)-featurette" || featurette.Extra == nil || featurette.Extra.Kind != domain.ExtraFeaturette {
		t.Errorf("extras: %+v, %+v", teaser, featurette)
	}
	if len(teaser.Images) == 0 || teaser.Images[0].Kind != domain.ImageThumb {
		t.Errorf("frame of the teaser: %+v", teaser.Images)
	}
	series := itemByKey(t, a, showLib, "series:Café Stories (2022)")
	_, seriesDetails, _, err := a.Series(ctx, p, series.ID)
	mustNil(t, err)
	showExtras, err := a.Extras(ctx, p, series.ID)
	mustNil(t, err)
	if seriesDetails.Extras != 1 || seriesDetails.Trailer != nil || len(showExtras) != 1 || showExtras[0].Item.Title != "Behind the counter" {
		t.Fatalf("extras of the series: %d %+v", seriesDetails.Extras, showExtras)
	}
	if _, err := a.Extras(ctx, p, showExtras[0].Item.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("extras of an extra: %v", err)
	}

	// Played from the start, nothing kept.
	info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: teaser.Item.ID, Audio: -1, Device: browser})
	mustNil(t, err)
	if info.Resume != 0 {
		t.Errorf("resume point of an extra: %s", info.Resume)
	}
	mustNil(t, a.ReportProgress(p, info.SessionID, info.Duration/2))
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, info.Duration/2))
	history, err := a.History(ctx, p, "", 10)
	mustNil(t, err)
	again, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: teaser.Item.ID, Audio: -1, Device: browser})
	mustNil(t, err)
	mustNil(t, a.StopPlayback(ctx, p, again.SessionID, 0))
	if len(history.Entries) != 0 || again.Resume != 0 {
		t.Errorf("history %+v, resume %s", history.Entries, again.Resume)
	}
	if found, err := a.Search(ctx, p, "Teaser", 10); err != nil || len(found) != 0 {
		t.Errorf("search: %+v %v", found, err)
	}
	if _, err := a.CreatePlaylist(ctx, p, "Bonus", []domain.ID{teaser.Item.ID}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("extra in a playlist: %v", err)
	}

	// As hidden as their movie.
	kid, kidProfile := p, *p.Profile
	kidProfile.Parental = domain.ParentalControl{BlockUnrated: true}
	kid.Profile = &kidProfile
	if _, err := a.Extras(ctx, kid, big.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("extras of a hidden movie: %v", err)
	}
	if _, err := a.StartPlayback(ctx, kid, PlayRequest{ItemID: teaser.Item.ID, Audio: -1, Device: browser}); !isKind(err, domain.ErrNotFound) {
		t.Errorf("extra of a hidden movie: %v", err)
	}
}
