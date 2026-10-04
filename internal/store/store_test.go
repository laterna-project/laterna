package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/migrations"
)

// openTemp opens a database in a folder whose name has a non-ASCII character, as a Windows user
// profile may have.
func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "donnée", FileName)
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, path
}

func TestOpenMigratesAndReopens(t *testing.T) {
	st, path := openTemp(t)
	ctx := context.Background()
	if err := st.Write(ctx, func(q Q) error {
		return q.SetSetting(ctx, "server.name", "Salon")
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening (migrations already applied): %v", err)
	}
	defer func() { _ = again.Close() }()
	got, ok, err := again.Read().Setting(ctx, "server.name")
	if err != nil || !ok || got != "Salon" {
		t.Fatalf("value read back %q, %v", got, err)
	}
}

func TestWriteRollsBackOnError(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := st.Write(ctx, func(q Q) error {
		if err := q.SetSetting(ctx, "k", "v"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("original error lost: %v", err)
	}
	if _, ok, err := st.Read().Setting(ctx, "k"); ok || err != nil {
		t.Fatalf("the transaction should have been rolled back: %v %v", ok, err)
	}
}

func TestReaderIsReadOnly(t *testing.T) {
	st, _ := openTemp(t)
	_, err := st.reader.ExecContext(context.Background(), "INSERT INTO settings (key, value) VALUES ('x', 'y')")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("the read pool must refuse writes, got %v", err)
	}
}

func TestInsertSettingIfAbsentKeepsExisting(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	for _, v := range []string{"premier", "second"} {
		if err := st.Write(ctx, func(q Q) error {
			return q.InitSetting(ctx, "server.id", v)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ := st.Read().Setting(ctx, "server.id"); got != "premier" {
		t.Errorf("existing value overwritten: %q", got)
	}
}

func TestConcurrentWritesAreSerialized(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			errs <- st.Write(ctx, func(q Q) error {
				return q.SetSetting(ctx, "compteur", string(rune('a'+i)))
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write failed (SQLITE_BUSY?): %v", err)
		}
	}
}

func TestOpenRejectsQueryCharacters(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "a?b.db")); err == nil {
		t.Fatal("a path with \"?\" would break the connection string: want an error")
	}
}

// Migration 00013 rebuilds libraries, items and images to widen their constraints (music): nothing
// is lost, search follows, foreign keys are restored and cascading deletes still work.
func TestMusicMigrationKeepsCatalog(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	lib := newLibrary("Films", domain.LibraryMovies, "/m")
	movie := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "k", Title: "Amélie", SortTitle: "amelie", AddedAt: t0, UpdatedAt: t0}
	mustWrite(t, st, func(q Q) error { return errors.Join(q.CreateLibrary(ctx, lib), q.CreateItem(ctx, movie)) })

	provider, err := goose.NewProvider(database.DialectSQLite3, st.writer, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var fk, fts int
	if err := st.writer.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign keys: %d %v", fk, err)
	}
	if err := st.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH '"amelie"*'`).Scan(&fts); err != nil || fts != 1 {
		t.Fatalf("search after the rebuild: %d %v", fts, err)
	}
	if it, err := st.Read().Item(ctx, movie.ID); err != nil || it.Title != "Amélie" {
		t.Fatalf("item lost: %+v %v", it, err)
	}
	music := newLibrary("Musique", domain.LibraryMusic, "/a")
	mustWrite(t, st, func(q Q) error { return q.CreateLibrary(ctx, music) })
	mustWrite(t, st, func(q Q) error { return q.DeleteLibrary(ctx, lib.ID) })
	if _, err := st.Read().Item(ctx, movie.ID); !IsNotFound(err) {
		t.Fatalf("cascading delete: %v", err)
	}
}

// Migration 00008 makes the metadata of every existing item be read again: one job per item, whose
// target is the ID in canonical form.
func TestMigrationReappliesMetadata(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	lib := newLibrary("Films", domain.LibraryMovies, "/m")
	movie := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "k", Title: "x", SortTitle: "x", AddedAt: t0, UpdatedAt: t0}
	mustWrite(t, st, func(q Q) error { return errors.Join(q.CreateLibrary(ctx, lib), q.CreateItem(ctx, movie)) })

	provider, err := goose.NewProvider(database.DialectSQLite3, st.writer, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	// A real deadline: a zero one would look like "no job" to the workers.
	if next, err := st.Read().NextJobTime(ctx, "io"); err != nil || next.IsZero() || next.After(time.Now()) || time.Since(next) > time.Minute {
		t.Fatalf("deadline: %v %v", next, err)
	}
	var job Job
	mustWrite(t, st, func(q Q) error {
		var ok bool
		var err error
		job, ok, err = q.ClaimJob(ctx, "io", time.Now())
		if !ok && err == nil {
			err = errors.New("no job")
		}
		return err
	})
	if job.Kind != "item.metadata" || job.Target != movie.ID.String() {
		t.Errorf("job: %+v, want target %s", job, movie.ID)
	}
}

// Read connections have a small page cache: memory that SQLite allocates is never given back to the
// OS.
func TestReaderCacheIsBounded(t *testing.T) {
	st, _ := openTemp(t)
	var n int
	if err := st.reader.QueryRowContext(context.Background(), "PRAGMA cache_size").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != -readerCache {
		t.Errorf("read cache: %d, want %d", n, -readerCache)
	}
}

// Migration 00023 rebuilds images to add themes. On the way down item images stay and theme images
// go with their themes; going up again works.
func TestThemesMigrationRoundTrip(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	lib := newLibrary("Films", domain.LibraryMovies, "/m")
	movie := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "k", Title: "x", SortTitle: "x", AddedAt: t0, UpdatedAt: t0}
	theme := domain.Theme{ID: domain.NewID(), Name: "Maison", CreatedAt: t0, UpdatedAt: t0}
	mustWrite(t, st, func(q Q) error {
		if err := errors.Join(q.CreateLibrary(ctx, lib), q.CreateItem(ctx, movie), q.CreateTheme(ctx, theme)); err != nil {
			return err
		}
		_, _, err := q.SetItemImage(ctx, movie.ID, domain.ImagePoster, domain.ImageLocal, "/m/poster.jpg", "", t0)
		return errors.Join(err, q.AddThemeImage(ctx, domain.Image{
			ID: domain.NewID(), ThemeID: &theme.ID, Kind: domain.ImageLogo, Path: "/meta/logo.png", Hash: "h", UpdatedAt: t0,
		}))
	})
	provider, err := goose.NewProvider(database.DialectSQLite3, st.writer, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	var images int
	if err := st.writer.QueryRowContext(ctx, "SELECT COUNT(*) FROM images").Scan(&images); err != nil || images != 1 {
		t.Fatalf("images after migrating down: %d %v", images, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if imgs, err := st.Read().ItemImages(ctx, movie.ID); err != nil || len(imgs) != 1 {
		t.Fatalf("item image lost: %v %v", imgs, err)
	}
	if themes, err := st.Read().Themes(ctx); err != nil || len(themes) != 0 {
		t.Fatalf("themes after migrating up again: %v %v", themes, err)
	}
}
