package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// waitMovies waits for the library to have n movies (ten seconds at most), without asking for a
// scan.
func waitMovies(t *testing.T, a *App, p domain.Principal, want int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		page, err := a.ListMovies(context.Background(), p, ListQuery{})
		mustNil(t, err)
		if page.Total == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d movies instead of %d: %v", page.Total, want, titles(page.Items))
		}
	}
}

// Folder watching: a movie added or removed shows up or goes away without a scan being asked for.
// With the setting off, nothing moves until the next scan.
func TestWatchLibraries(t *testing.T) {
	a, _ := startMediaApp(t, func(o *Options) {
		o.NoAutoScans = false
		o.WatchQuiet = 200 * time.Millisecond
	})
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	root := t.TempDir()
	copyTree(t, filepath.Join(testRoot("Movies"), "Big Test Movie (2020)"), filepath.Join(root, "Big Test Movie (2020)"))
	_, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{root}, "")
	mustNil(t, err)
	waitIdle(t, a)
	waitMovies(t, a, p, 1)

	// A movie folder is added: scanned on its own.
	copyTree(t, filepath.Join(testRoot("Movies"), "Dual Audio (2019)"), filepath.Join(root, "Dual Audio (2019)"))
	waitMovies(t, a, p, 2)
	// A file is removed: the movie goes away (file recorded as missing).
	mustNil(t, os.RemoveAll(filepath.Join(root, "Big Test Movie (2020)")))
	waitMovies(t, a, p, 1)

	// Setting off: a new movie waits for the next scan.
	off := false
	_, err = a.UpdateSettings(ctx, p, SettingsChanges{WatchLibraries: &off})
	mustNil(t, err)
	time.Sleep(300 * time.Millisecond) // watching stops
	copyTree(t, filepath.Join(testRoot("Movies"), "Stream Dump (2018)"), filepath.Join(root, "Stream Dump (2018)"))
	time.Sleep(time.Second)
	waitIdle(t, a)
	waitMovies(t, a, p, 1)
	// Back on: watching resumes (and the next change picks up the one that was missed).
	on := true
	_, err = a.UpdateSettings(ctx, p, SettingsChanges{WatchLibraries: &on})
	mustNil(t, err)
	time.Sleep(300 * time.Millisecond)
	copyTree(t, filepath.Join(testRoot("Movies"), "Versions (2017)"), filepath.Join(root, "Versions (2017)"))
	waitMovies(t, a, p, 3)
}
