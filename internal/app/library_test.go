package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// copyTree copies part of the fixtures into a temporary folder that a test may change.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		return errors.Join(err, out.Close())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// startMediaApp starts an application with ffprobe and background work. with adjusts the options.
func startMediaApp(t *testing.T, with ...func(*Options)) (*App, *clock) {
	t.Helper()
	testfixtures.Library(t) // generates the fixtures, or skips the test without FFmpeg
	ffmpeg, ffprobe, _ := testfixtures.FFmpeg()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		ServerName: "Test", FFmpeg: ffmpeg, FFprobe: ffprobe, NoAutoScans: true, CacheDir: t.TempDir(),
	}
	for _, f := range with {
		f(&opts)
	}
	a, err := New(ctx, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	c := newClock()
	a.now = c.now
	runCtx, cancel := context.WithCancel(ctx)
	if err := a.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		a.Wait()
		_ = st.Close()
	})
	return a, c
}

// waitIdle waits for the job queue to be empty, and fails if a job failed.
func waitIdle(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		counts, err := a.store.Read().CountJobs(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		busy := false
		for _, c := range counts {
			if c.State == "failed" {
				failed, _ := a.store.Read().FailedJobs(context.Background(), 10)
				t.Fatalf("failed job: %+v — %+v", c, failed)
			}
			busy = true
		}
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job queue never empty: %+v", counts)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func itemByKey(t *testing.T, a *App, lib domain.Library, key string) domain.Item {
	t.Helper()
	it, err := a.store.Read().ItemByGroupKey(context.Background(), lib.ID, key)
	if err != nil {
		t.Fatalf("item %q: %v", key, err)
	}
	return it
}

func counts(t *testing.T, a *App, lib domain.Library) map[domain.ItemKind]int {
	t.Helper()
	c, err := a.store.Read().LibraryItemCounts(context.Background(), lib.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMoviesLibraryEndToEnd(t *testing.T) {
	a, c := startMediaApp(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "Films")
	copyTree(t, filepath.Join(testfixtures.Root(), "Films"), root)

	lib, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{root}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if got := counts(t, a, lib); got[domain.ItemMovie] != 5 {
		t.Fatalf("movies: %v", got)
	}

	big := itemByKey(t, a, lib, "movie:Big Test Movie (2020)/bigtestmovie")
	if big.Title != "Big Test Movie" || big.Year != 2020 || big.Overview == "" || big.Runtime != time.Minute {
		t.Errorf("metadata from the NFO: %+v", big)
	}
	details, err := a.store.Read().Details(ctx, big.ID)
	if err != nil || len(details.Genres) != 1 || details.Genres[0] != "Aventure" {
		t.Errorf("genres: %+v %v", details, err)
	}
	imgs, err := a.store.Read().ItemImages(ctx, big.ID)
	if err != nil || len(imgs) != 2 {
		t.Fatalf("images: %+v %v", imgs, err)
	}
	for _, img := range imgs {
		if img.BlurHash == "" || img.Width == 0 || img.Hash == "" {
			t.Errorf("image not analyzed: %+v", img)
		}
	}

	versions, _ := a.store.Read().ItemFiles(ctx, itemByKey(t, a, lib, "movie:Versions (2017)/versions").ID)
	if len(versions) != 2 || versions[0].Version != "1080p" || versions[1].Version != "720p" {
		t.Errorf("versions: %+v", versions)
	}
	multi, _ := a.store.Read().ItemFiles(ctx, itemByKey(t, a, lib, "movie:Deux Pistes (2019)/deuxpistes").ID)
	if len(multi) != 1 || len(multi[0].File.Info.Streams) != 5 || len(multi[0].File.Info.Chapters) != 3 {
		t.Errorf("analysis of the multi-track MKV: %+v", multi)
	}

	// Renamed folder: same item, same file.
	bigFiles, err := a.store.Read().ItemFiles(ctx, big.ID)
	if err != nil || len(bigFiles) != 1 {
		t.Fatalf("files before the rename: %+v (%v)", bigFiles, err)
	}
	if err := os.Rename(filepath.Join(root, "Big Test Movie (2020)"), filepath.Join(root, "Big Test Movie Renamed (2020)")); err != nil {
		t.Fatal(err)
	}
	if err := a.ScanLibrary(ctx, lib.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	renamed := itemByKey(t, a, lib, "movie:Big Test Movie Renamed (2020)/bigtestmovie")
	if renamed.ID != big.ID {
		t.Errorf("renaming the folder created another item (%s ≠ %s): the history would be lost", renamed.ID, big.ID)
	}
	if files, _ := a.store.Read().ItemFiles(ctx, big.ID); len(files) != 1 || files[0].File.ID != bigFiles[0].File.ID {
		t.Errorf("file after the rename: %+v", files)
	}

	// Deleted movie: kept during the grace period, then forgotten.
	if err := os.RemoveAll(filepath.Join(root, "Sans Index (2018)")); err != nil {
		t.Fatal(err)
	}
	if err := a.ScanLibrary(ctx, lib.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if got := counts(t, a, lib); got[domain.ItemMovie] != 5 {
		t.Errorf("during the grace period the movie stays: %v", got)
	}
	c.advance(73 * time.Hour)
	if err := a.ScanLibrary(ctx, lib.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if got := counts(t, a, lib); got[domain.ItemMovie] != 4 {
		t.Errorf("after the grace period: %v", got)
	}
}

func TestOfflineRootKeepsEverything(t *testing.T) {
	a, c := startMediaApp(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "Films")
	copyTree(t, filepath.Join(testfixtures.Root(), "Films", "Versions (2017)"), filepath.Join(root, "Versions (2017)"))
	lib, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{root}, "fr-FR")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)

	check := func(label string) {
		t.Helper()
		files, err := a.store.Read().LibraryFiles(ctx, lib.ID)
		if err != nil || len(files) != 2 {
			t.Fatalf("%s: files %+v %v", label, files, err)
		}
		for _, f := range files {
			if f.MissingSince != nil {
				t.Errorf("%s: %s marked missing", label, f.Path)
			}
		}
	}
	check("start")

	// Unplugged share: the root no longer exists.
	hidden := root + "-unplugged"
	if err := os.Rename(root, hidden); err != nil {
		t.Fatal(err)
	}
	c.advance(100 * time.Hour)
	if err := a.ScanLibrary(ctx, lib.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	check("racine absente")

	// Empty mount point (share not mounted): same caution.
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := a.ScanLibrary(ctx, lib.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	check("racine vide")
	if got := counts(t, a, lib); got[domain.ItemMovie] != 1 {
		t.Errorf("movie lost: %v", got)
	}
}

func TestShowsLibraryEndToEnd(t *testing.T) {
	a, _ := startMediaApp(t)
	ctx := context.Background()
	base := t.TempDir()
	series := filepath.Join(base, "Séries")
	animes := filepath.Join(base, "Animes")
	copyTree(t, filepath.Join(testfixtures.Root(), "Séries"), series)
	copyTree(t, filepath.Join(testfixtures.Root(), "Animes"), animes)

	lib, err := a.CreateLibrary(ctx, "Séries", domain.LibraryShows, []string{series, animes}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	got := counts(t, a, lib)
	if got[domain.ItemSeries] != 2 || got[domain.ItemSeason] != 3 || got[domain.ItemEpisode] != 7 {
		t.Fatalf("catalog: %v", got)
	}

	show := itemByKey(t, a, lib, "series:Série Test (2022)")
	if show.Title != "Série Test" || show.Year != 2022 || show.Overview == "" {
		t.Errorf("series (tvshow.nfo): %+v", show)
	}
	if imgs, _ := a.store.Read().ItemImages(ctx, show.ID); len(imgs) != 1 || imgs[0].Kind != domain.ImagePoster {
		t.Errorf("series poster: %+v", imgs)
	}
	ep := itemByKey(t, a, lib, "episode:"+show.ID.String()+":1:2")
	if ep.Title != "Épisode 2" || ep.Runtime == 0 {
		t.Errorf("episode (NFO): %+v", ep)
	}
	anime := itemByKey(t, a, lib, "series:Anime Test")
	animeEp := itemByKey(t, a, lib, "episode:"+anime.ID.String()+":1:3")
	info, err := a.store.Read().Episode(ctx, animeEp.ID)
	if err != nil || !info.Absolute || info.Number != 3 {
		t.Errorf("anime with absolute numbering: %+v %v", info, err)
	}
}

func TestLibraryValidation(t *testing.T) {
	a, _ := newTestApp(t)
	ctx := context.Background()
	dir := t.TempDir()
	cases := []struct {
		name  string
		kind  domain.LibraryKind
		paths []string
		want  error
	}{
		{"", domain.LibraryMovies, []string{dir}, domain.ErrInvalid},
		{"X", "musique", []string{dir}, domain.ErrInvalid},
		{"X", domain.LibraryMovies, nil, domain.ErrInvalid},
		{"X", domain.LibraryMovies, []string{"relatif/films"}, domain.ErrInvalid},
		{"X", domain.LibraryMovies, []string{filepath.Join(dir, "absent")}, domain.ErrInvalid},
		{"X", domain.LibraryMovies, []string{dir, filepath.Join(dir, "sous")}, domain.ErrInvalid},
	}
	if err := os.MkdirAll(filepath.Join(dir, "sous"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if _, err := a.CreateLibrary(ctx, tc.name, tc.kind, tc.paths, ""); !errors.Is(err, tc.want) {
			t.Errorf("%q %v %v: %v", tc.name, tc.kind, tc.paths, err)
		}
	}
	if _, err := a.CreateLibrary(ctx, "Films", domain.LibraryMovies, []string{dir}, "fr-FR"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateLibrary(ctx, "Autre", domain.LibraryShows, []string{filepath.Join(dir, "sous")}, ""); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("folder in another library: %v", err)
	}
	if _, err := a.CreateLibrary(ctx, "films", domain.LibraryShows, []string{t.TempDir()}, ""); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
}
