package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestJobsQueue(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	now := t0
	enqueue := func(kind, target string, prio int, at time.Time) {
		t.Helper()
		mustWrite(t, st, func(q Q) error {
			return q.EnqueueJob(ctx, JobRequest{Kind: kind, Target: target, Class: "io", Priority: prio, RunAfter: at}, now)
		})
	}
	claim := func() (Job, bool) {
		t.Helper()
		var j Job
		var ok bool
		mustWrite(t, st, func(q Q) error {
			var err error
			j, ok, err = q.ClaimJob(ctx, "io", now)
			return err
		})
		return j, ok
	}

	enqueue("file.analyze", "a", 0, now)
	enqueue("file.analyze", "a", 5, now) // merged: priority raised
	enqueue("file.analyze", "b", 1, now)
	enqueue("file.analyze", "later", 9, now.Add(time.Hour))

	first, ok := claim()
	if !ok || first.Target != "a" || first.Attempts != 1 {
		t.Fatalf("first job: %+v %v (highest priority, deduplicated)", first, ok)
	}
	// While "a" is running, a new request for "a" can wait next to it.
	enqueue("file.analyze", "a", 0, now)
	second, _ := claim()
	third, _ := claim()
	if second.Target != "b" || third.Target != "a" {
		t.Fatalf("order: %q then %q", second.Target, third.Target)
	}
	if _, ok := claim(); ok {
		t.Fatal("the future job must not be ready")
	}
	if next, _ := st.Read().NextJobTime(ctx, "io"); !next.Equal(now.Add(time.Hour)) {
		t.Errorf("next deadline: %v", next)
	}

	// Retry after a failure: the waiting twin does the work and the retry is removed.
	enqueue("file.analyze", "b", 0, now)
	mustWrite(t, st, func(q Q) error { return q.RetryJob(ctx, second.ID, now.Add(time.Minute), "boom", now) })
	mustWrite(t, st, func(q Q) error { return q.CompleteJob(ctx, first.ID) })
	mustWrite(t, st, func(q Q) error { return q.FailJob(ctx, third.ID, "unrecoverable", now) })

	counts, err := st.Read().CountJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range counts {
		got[c.State] += c.N
	}
	if got["pending"] != 2 || got["failed"] != 1 || got["running"] != 0 {
		t.Errorf("count: %v", counts)
	}

	// Hard stop: "running" jobs go back to waiting.
	now = now.Add(2 * time.Hour)
	running, _ := claim()
	mustWrite(t, st, func(q Q) error { return q.RequeueRunningJobs(ctx, now) })
	again, ok := claim()
	if !ok || again.Target != running.Target || again.Attempts != 2 {
		t.Errorf("after requeue: %+v %v", again, ok)
	}
}

func newLibrary(name string, kind domain.LibraryKind, paths ...string) domain.Library {
	return domain.Library{ID: domain.NewID(), Name: name, Kind: kind, Paths: paths, Language: "fr-FR", CreatedAt: t0, UpdatedAt: t0}
}

func TestLibrariesAndPaths(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	films := newLibrary("Movies", domain.LibraryMovies, "/media/movies", "/media/movies2")
	mustWrite(t, st, func(q Q) error { return q.CreateLibrary(ctx, films) })

	dup := newLibrary("Other", domain.LibraryShows, "/media/movies")
	if err := st.Write(ctx, func(q Q) error { return q.CreateLibrary(ctx, dup) }); !errors.Is(err, ErrDuplicate) {
		t.Errorf("folder already in use: %v", err)
	}
	got, err := st.Read().Library(ctx, films.ID)
	if err != nil || got.Kind != domain.LibraryMovies || !slices.Equal(got.Paths, []string{"/media/movies", "/media/movies2"}) {
		t.Fatalf("library: %+v %v", got, err)
	}
	films.Paths = []string{"/media/cinema"}
	films.Name = "Cinema"
	mustWrite(t, st, func(q Q) error { return q.UpdateLibrary(ctx, films) })
	all, _ := st.Read().Libraries(ctx)
	if len(all) != 1 || all[0].Name != "Cinema" || !slices.Equal(all[0].Paths, []string{"/media/cinema"}) {
		t.Errorf("after the update: %+v", all)
	}
}

// A movie with two files and a series with one episode: files are deleted, then items cleaned up.
func TestItemsFilesAndOrphans(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	lib := newLibrary("All", domain.LibraryShows, "/m")
	movie := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "Movie/movie", Title: "Movie", SortTitle: "movie", AddedAt: t0, UpdatedAt: t0}
	series := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemSeries, GroupKey: "series:Show", Title: "Show", SortTitle: "show", AddedAt: t0, UpdatedAt: t0}
	season := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemSeason, ParentID: &series.ID, GroupKey: "season:1", Title: "Season 1", SortTitle: "0001", AddedAt: t0, UpdatedAt: t0}
	episode := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemEpisode, ParentID: &season.ID, GroupKey: "episode:1:1", Title: "Episode 1", SortTitle: "0001", AddedAt: t0, UpdatedAt: t0}
	files := []domain.MediaFile{
		{ID: domain.NewID(), LibraryID: lib.ID, Path: "/m/Movie/a.mkv", Size: 1, ModTime: t0, Fingerprint: "fa"},
		{ID: domain.NewID(), LibraryID: lib.ID, Path: "/m/Movie/b.mkv", Size: 1, ModTime: t0, Fingerprint: "fb"},
		{ID: domain.NewID(), LibraryID: lib.ID, Path: "/m/Show/e1.mkv", Size: 1, ModTime: t0, Fingerprint: "fe"},
	}
	mustWrite(t, st, func(q Q) error {
		steps := []error{q.CreateLibrary(ctx, lib)}
		for _, it := range []domain.Item{movie, series, season, episode} {
			steps = append(steps, q.CreateItem(ctx, it))
		}
		steps = append(steps,
			q.CreateSeason(ctx, domain.Season{ItemID: season.ID, SeriesID: series.ID, Number: 1}),
			q.CreateEpisode(ctx, domain.Episode{ItemID: episode.ID, SeriesID: series.ID, SeasonID: season.ID, SeasonNumber: 1, Number: 1}),
		)
		for _, f := range files {
			steps = append(steps, q.CreateFile(ctx, f, t0))
		}
		steps = append(steps,
			q.LinkFile(ctx, movie.ID, files[0].ID, "1080p", 0),
			q.LinkFile(ctx, movie.ID, files[1].ID, "720p", 0),
			q.LinkFile(ctx, episode.ID, files[2].ID, "", 0),
			q.SetFileAnalysis(ctx, files[0].ID, domain.MediaInfo{
				Container: "mkv", Duration: 90 * time.Minute,
				Streams:  []domain.Stream{{Index: 0, Kind: domain.StreamVideo, Codec: "h264", Width: 1920, Height: 1080}, {Index: 1, Kind: domain.StreamAudio, Codec: "aac", Language: "fre", Default: true}},
				Chapters: []domain.Chapter{{Start: 0, End: time.Minute, Title: "OP"}},
			}, t0),
		)
		return errors.Join(steps...)
	})

	itemFiles, err := st.Read().ItemFiles(ctx, movie.ID)
	if err != nil || len(itemFiles) != 2 || itemFiles[0].Version != "1080p" {
		t.Fatalf("files of the movie: %+v %v", itemFiles, err)
	}
	f := itemFiles[0].File
	if f.AnalyzedAt == nil || f.Info.Duration != 90*time.Minute || len(f.Info.Streams) != 2 || !f.Info.Streams[1].Default || f.Info.Chapters[0].Title != "OP" {
		t.Errorf("analysis read back: %+v", f)
	}
	if ep, err := st.Read().Episode(ctx, episode.ID); err != nil || ep.SeasonNumber != 1 || ep.SeriesID != series.ID {
		t.Errorf("episode: %+v %v", ep, err)
	}

	// No file left: the movie and the episode go, then the season and the series.
	var removed int64
	mustWrite(t, st, func(q Q) error {
		for _, f := range files {
			if err := q.DeleteFile(ctx, f.ID); err != nil {
				return err
			}
		}
		var err error
		removed, err = q.DeleteOrphanItems(ctx, lib.ID)
		return err
	})
	if removed != 4 {
		t.Errorf("%d items deleted, want 4", removed)
	}
}

func TestMetadataAndImages(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	lib := newLibrary("Movies", domain.LibraryMovies, "/m")
	movie := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "k", Title: "x", SortTitle: "x", AddedAt: t0, UpdatedAt: t0}
	mustWrite(t, st, func(q Q) error { return errors.Join(q.CreateLibrary(ctx, lib), q.CreateItem(ctx, movie)) })

	meta := domain.Metadata{
		Title: "Amélie", SortTitle: "amelie", Year: 2001, Overview: "…", CommunityRating: 7.9, Runtime: 122 * time.Minute,
		Genres: []string{"Romance", "Comedy"}, ProviderIDs: map[string]string{"tmdb": "194"},
		Credits: []domain.Credit{{Name: "Audrey Tautou", Role: domain.RoleActor, Character: "Amélie"}, {Name: "Jean-Pierre Jeunet", Role: domain.RoleDirector}},
	}
	var people []domain.ID
	for range 2 { // idempotent
		mustWrite(t, st, func(q Q) error {
			var err error
			people, err = q.SetMetadata(ctx, movie.ID, meta, t0)
			return err
		})
	}
	if len(people) != 2 || people[0] == people[1] {
		t.Fatalf("people: %v", people)
	}

	it, _ := st.Read().Item(ctx, movie.ID)
	if it.Title != "Amélie" || it.Year != 2001 || it.Runtime != 122*time.Minute || it.MetadataAt == nil {
		t.Errorf("item: %+v", it)
	}
	d, err := st.Read().Details(ctx, movie.ID)
	if err != nil || !slices.Equal(d.Genres, []string{"Comedy", "Romance"}) || d.ProviderIDs["tmdb"] != "194" || len(d.Credits) != 2 {
		t.Errorf("details: %+v %v", d, err)
	}

	var img domain.Image
	var changed bool
	mustWrite(t, st, func(q Q) error {
		var err error
		img, changed, err = q.SetItemImage(ctx, movie.ID, domain.ImagePoster, domain.ImageLocal, "/m/poster.jpg", "", t0)
		return err
	})
	if !changed {
		t.Error("new image: must be analyzed")
	}
	mustWrite(t, st, func(q Q) error { return q.SetImageAnalysis(ctx, img.ID, 600, 900, "LEHV6nWB2yk8", "abc", t0) })
	mustWrite(t, st, func(q Q) error {
		var err error
		_, changed, err = q.SetItemImage(ctx, movie.ID, domain.ImagePoster, domain.ImageLocal, "/m/poster.jpg", "", t0)
		return err
	})
	if changed {
		t.Error("same image already analyzed: nothing to redo")
	}
	mustWrite(t, st, func(q Q) error {
		var err error
		img, changed, err = q.SetItemImage(ctx, movie.ID, domain.ImagePoster, domain.ImageLocal, "/m/folder.jpg", "", t0)
		return err
	})
	if !changed || img.Hash != "" {
		t.Errorf("replaced image: %+v %v", img, changed)
	}
	if imgs, _ := st.Read().ItemImages(ctx, movie.ID); len(imgs) != 1 || imgs[0].Path != "/m/folder.jpg" {
		t.Errorf("images: %+v", imgs)
	}

	// Photo of a person: served with the credits once it is analyzed.
	mustWrite(t, st, func(q Q) error {
		var err error
		img, changed, err = q.SetPersonImage(ctx, people[0], "/meta/audrey.jpg", "https://x.test/audrey.jpg", t0)
		return err
	})
	if !changed || img.PersonID == nil || *img.PersonID != people[0] {
		t.Fatalf("photo: %+v %v", img, changed)
	}
	if d, _ := st.Read().Details(ctx, movie.ID); d.Credits[0].Image != nil {
		t.Errorf("photo served before being analyzed: %+v", d.Credits[0])
	}
	mustWrite(t, st, func(q Q) error { return q.SetImageAnalysis(ctx, img.ID, 300, 450, "LEHV6nWB2yk8", "def", t0) })
	d, _ = st.Read().Details(ctx, movie.ID)
	if c := d.Credits[0]; c.Name != "Audrey Tautou" || c.Image == nil || c.Image.ID != img.ID || c.Image.Hash != "def" || c.Image.Width != 300 {
		t.Errorf("credits: %+v", c)
	}
	if paths, _ := st.Read().RemoteImagePaths(ctx); !slices.Equal(paths, []string{"/meta/audrey.jpg"}) {
		t.Errorf("downloaded images: %v", paths)
	}

	// People without credits are forgotten, along with their photo.
	mustWrite(t, st, func(q Q) error {
		_, err := q.SetMetadata(ctx, movie.ID, domain.Metadata{Title: "Amélie", SortTitle: "amelie"}, t0)
		return err
	})
	mustWrite(t, st, func(q Q) error {
		n, err := q.DeleteOrphanPeople(ctx)
		if n != 2 {
			t.Errorf("%d people forgotten, want 2", n)
		}
		return err
	})
	if paths, _ := st.Read().RemoteImagePaths(ctx); len(paths) != 0 {
		t.Errorf("photo still there: %v", paths)
	}
}

func TestMetadataDirs(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	lib := newLibrary("Movies", domain.LibraryMovies, "/m")
	mustWrite(t, st, func(q Q) error {
		return errors.Join(q.CreateLibrary(ctx, lib), q.SetMetadataDir(ctx, lib.ID, "/m/a", "1"), q.SetMetadataDir(ctx, lib.ID, "/m/b", "2"))
	})
	mustWrite(t, st, func(q Q) error {
		return errors.Join(q.SetMetadataDir(ctx, lib.ID, "/m/a", "3"), q.SetMetadataDir(ctx, lib.ID, "/m/b", ""))
	})
	if dirs, err := st.Read().MetadataDirs(ctx, lib.ID); err != nil || !maps.Equal(dirs, map[string]string{"/m/a": "3"}) {
		t.Errorf("folders: %v %v", dirs, err)
	}
}

// Library order: by kind then by name as long as nobody ranked them; after that the chosen order,
// with a library created later coming after the others.
func TestLibraryOrder(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	names := func() []string {
		t.Helper()
		libs, err := st.Read().Libraries(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(libs))
		for i, l := range libs {
			out[i] = l.Name
		}
		return out
	}
	byName := map[string]domain.ID{}
	create := func(name string, kind domain.LibraryKind) {
		t.Helper()
		l := newLibrary(name, kind)
		byName[name] = l.ID
		mustWrite(t, st, func(q Q) error { return q.CreateLibrary(ctx, l) })
	}
	create("Photos", domain.LibraryPhotos)
	create("Anime", domain.LibraryShows)
	create("Music", domain.LibraryMusic)
	create("Movies", domain.LibraryMovies)
	create("Books", domain.LibraryBooks)
	create("Cartoons", domain.LibraryMovies)
	if got := names(); !slices.Equal(got, []string{"Cartoons", "Movies", "Anime", "Music", "Books", "Photos"}) {
		t.Fatalf("default order: %v", got)
	}

	order := []string{"Anime", "Movies", "Cartoons", "Photos", "Music", "Books"}
	mustWrite(t, st, func(q Q) error {
		ids := make([]domain.ID, len(order))
		for i, name := range order {
			ids[i] = byName[name]
		}
		return q.SetLibraryOrder(ctx, ids)
	})
	if got := names(); !slices.Equal(got, order) {
		t.Fatalf("chosen order: %v", got)
	}
	libs, err := st.Read().Libraries(ctx)
	if err != nil || len(libs) != 6 {
		t.Fatalf("libraries: %v %v", libs, err)
	}
	if libs[0].Position != 1 || libs[5].Position != 6 {
		t.Errorf("ranks: %d ... %d", libs[0].Position, libs[5].Position)
	}

	// Created afterwards: after the ranked ones, by kind then by name.
	create("Shows", domain.LibraryShows)
	create("Concerts", domain.LibraryMovies)
	if got := names(); !slices.Equal(got, append(slices.Clone(order), "Concerts", "Shows")) {
		t.Errorf("after two more libraries: %v", got)
	}
}
