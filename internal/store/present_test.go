package store

import (
	"context"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// items.present follows the files of each item: linked, moved to another item, missing, back,
// forgotten.
func TestPresentFollowsFiles(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	now := time.Now()
	lib := newLibrary("Films", domain.LibraryMovies, "/m")
	a := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "a", Title: "A", SortTitle: "a", AddedAt: now, UpdatedAt: now}
	b := domain.Item{ID: domain.NewID(), LibraryID: lib.ID, Kind: domain.ItemMovie, GroupKey: "b", Title: "B", SortTitle: "b", AddedAt: now, UpdatedAt: now}
	f := domain.MediaFile{ID: domain.NewID(), LibraryID: lib.ID, Path: "/m/a.mkv", Size: 1, ModTime: now, Fingerprint: "x"}
	write := func(fn func(q Q) error) {
		t.Helper()
		if err := st.Write(ctx, fn); err != nil {
			t.Fatal(err)
		}
	}
	present := func(id domain.ID) bool {
		t.Helper()
		var p int
		if err := st.reader.QueryRowContext(ctx, "SELECT present FROM items WHERE id = ?", id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p == 1
	}
	write(func(q Q) error {
		if err := q.CreateLibrary(ctx, lib); err != nil {
			return err
		}
		if err := q.CreateItem(ctx, a); err != nil {
			return err
		}
		if err := q.CreateItem(ctx, b); err != nil {
			return err
		}
		return q.CreateFile(ctx, f, now)
	})
	if present(a.ID) || present(b.ID) {
		t.Fatal("present without a file")
	}
	write(func(q Q) error { return q.LinkFile(ctx, a.ID, f.ID, "", 0) })
	if !present(a.ID) {
		t.Error("linked: not present")
	}
	// The file moves to another item.
	write(func(q Q) error { return q.LinkFile(ctx, b.ID, f.ID, "", 0) })
	if present(a.ID) || !present(b.ID) {
		t.Errorf("moved: A %v, B %v", present(a.ID), present(b.ID))
	}
	write(func(q Q) error { return q.MarkFileMissing(ctx, f.ID, now) })
	if present(b.ID) {
		t.Error("missing: still present")
	}
	write(func(q Q) error { return q.MarkFilePresent(ctx, f.ID, now) })
	if !present(b.ID) {
		t.Error("back: not present")
	}
	write(func(q Q) error { return q.DeleteFile(ctx, f.ID) })
	if present(b.ID) {
		t.Error("forgotten: still present")
	}
}
