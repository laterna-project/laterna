package app

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

func TestCollectionsFromNFOAndByHand(t *testing.T) {
	if testing.Short() {
		t.Skip("full scan")
	}
	a, _ := startMediaApp(t)
	ctx := context.Background()
	_, admin := setupAdmin(t, a)
	root := t.TempDir()
	saga := `<set tmdbcolid="42"><name>Saga Test</name><overview>Two movies.</overview></set>`
	for name, set := range map[string]string{"Big Test Movie (2020)": saga, "Versions (2017)": saga} {
		dir := filepath.Join(root, name)
		copyTree(t, filepath.Join(testfixtures.Root(), "Movies", name), dir)
		writeText(t, filepath.Join(dir, "movie.nfo"), "<movie><title>"+name[:len(name)-7]+"</title>"+set+"</movie>")
	}
	films, err := a.CreateLibrary(ctx, "Movies", domain.LibraryMovies, []string{root}, "")
	mustNil(t, err)
	_, err = a.CreateLibrary(ctx, "Shows", domain.LibraryShows, []string{testRoot("Shows")}, "")
	mustNil(t, err)
	waitIdle(t, a)

	list, err := a.Collections(ctx, admin, nil)
	mustNil(t, err)
	if len(list) != 1 || list[0].Name != "Saga Test" || list[0].Manual || list[0].ItemCount != 2 || list[0].Overview != "Two movies." {
		t.Fatalf("collections: %+v", list)
	}
	c, items, err := a.Collection(ctx, admin, list[0].ID)
	mustNil(t, err)
	if len(items) != 2 {
		t.Fatalf("items: %v", titles(items))
	}
	if c.ItemCount != 2 || titles(items)[0] != "Versions" {
		t.Errorf("by release date: %v", titles(items))
	}
	movie := items[1].Item
	_, d, err := a.Movie(ctx, admin, movie.ID)
	mustNil(t, err)
	if len(d.Collections) != 1 || d.Collections[0].Name != "Saga Test" {
		t.Errorf("details: %+v", d.Collections)
	}
	// NFO-based collection: cannot be edited by hand.
	name := "Other"
	if _, err := a.UpdateCollection(ctx, c.ID, CollectionChanges{Name: &name}); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("editing an NFO-based collection: %v", err)
	}
	if err := a.DeleteCollection(ctx, c.ID); !isKind(err, domain.ErrPrecondition) {
		t.Errorf("deleting an NFO-based collection: %v", err)
	}

	// The NFO no longer names the collection: the movie leaves it at the next scan.
	writeText(t, filepath.Join(root, "Versions (2017)", "movie.nfo"), "<movie><title>Versions</title></movie>")
	rescan(t, a, films.ID)
	if _, items, _ = a.Collection(ctx, admin, c.ID); len(items) != 1 || items[0].Item.ID != movie.ID {
		t.Errorf("after the NFO change: %v", titles(items))
	}

	// Manual collection: a movie and a series, then a removal, then deletion.
	series, err := a.ListSeries(ctx, admin, ListQuery{})
	mustNil(t, err)
	mine, err := a.CreateCollection(ctx, "Chloé's picks", "", []domain.ID{movie.ID, series.Items[0].Item.ID})
	mustNil(t, err)
	if _, err := a.CreateCollection(ctx, "Invalid", "", []domain.ID{domain.NewID()}); !isKind(err, domain.ErrInvalid) {
		t.Errorf("unknown item: %v", err)
	}
	_, items, err = a.Collection(ctx, admin, mine.ID)
	mustNil(t, err)
	if len(items) != 2 {
		t.Errorf("selection: %v", titles(items))
	}
	renamed := "Selection"
	_, err = a.UpdateCollection(ctx, mine.ID, CollectionChanges{Name: &renamed, Remove: []domain.ID{movie.ID}})
	mustNil(t, err)
	if c, items, _ := a.Collection(ctx, admin, mine.ID); c.Name != "Selection" || len(items) != 1 {
		t.Errorf("after the update: %q %v", c.Name, titles(items))
	}
	// A kid profile (unrated hidden) sees none of these collections.
	kid, err := a.CreateProfile(ctx, admin, "Tom", "", true, nil, "")
	mustNil(t, err)
	onKid := admin
	onKid.Profile = &kid
	if list, _ := a.Collections(ctx, onKid, nil); len(list) != 0 {
		t.Errorf("a kid's collections: %+v", list)
	}
	if _, _, err := a.Collection(ctx, onKid, mine.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("invisible collection: %v", err)
	}
	mustNil(t, a.DeleteCollection(ctx, mine.ID))
	if list, _ := a.Collections(ctx, admin, nil); len(list) != 1 {
		t.Errorf("after deletion: %+v", list)
	}
}

func TestPlaylistsOfAProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("full scan")
	}
	a, _, p, movies := moviesByTitle(t)
	ctx := context.Background()
	_, err := a.CreateLibrary(ctx, "Shows", domain.LibraryShows, []string{testRoot("Shows")}, "")
	mustNil(t, err)
	waitIdle(t, a)
	series, err := a.ListSeries(ctx, p, ListQuery{})
	mustNil(t, err)
	show := series.Items[0].Item.ID

	// A whole series: its episodes, in order.
	pl, err := a.CreatePlaylist(ctx, p, "Marathon", []domain.ID{show})
	mustNil(t, err)
	if pl.EntryCount != 4 || pl.Duration == 0 || len(pl.Images) == 0 {
		t.Errorf("playlist created: %+v", pl)
	}
	movie := movies["Big Test Movie"].ID
	_, err = a.AddToPlaylist(ctx, p, pl.ID, []domain.ID{movie}, 0)
	mustNil(t, err)
	pl, err = a.AddToPlaylist(ctx, p, pl.ID, []domain.ID{movie}, -1) // the same movie, at the end
	mustNil(t, err)
	_, entries, err := a.PlaylistWithEntries(ctx, p, pl.ID)
	mustNil(t, err)
	if len(entries) != 6 || entries[0].Item.Item.ID != movie || entries[5].Item.Item.ID != movie || entries[1].Item.Episode == nil ||
		entries[1].Item.Episode.Number != 1 || entries[4].Item.Episode.SeasonNumber != 2 || pl.EntryCount != 6 {
		t.Fatalf("entries: %d, %+v", len(entries), pl)
	}
	// Move the first entry to the end, remove the last one.
	_, err = a.MovePlaylistEntry(ctx, p, pl.ID, entries[0].ID, 99)
	mustNil(t, err)
	_, err = a.RemoveFromPlaylist(ctx, p, pl.ID, []domain.ID{entries[5].ID})
	mustNil(t, err)
	_, after, err := a.PlaylistWithEntries(ctx, p, pl.ID)
	mustNil(t, err)
	var got []domain.ID
	for _, e := range after {
		got = append(got, e.ID)
	}
	if want := []domain.ID{entries[1].ID, entries[2].ID, entries[3].ID, entries[4].ID, entries[0].ID}; !slices.Equal(got, want) {
		t.Errorf("after move and removal: %v, want %v", got, want)
	}
	if _, err := a.RemoveFromPlaylist(ctx, p, pl.ID, []domain.ID{domain.NewID()}); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
	renamed, err := a.RenamePlaylist(ctx, p, pl.ID, "Watch all")
	mustNil(t, err)
	if renamed.Name != "Watch all" || renamed.EntryCount != 5 {
		t.Errorf("renamed: %+v", renamed)
	}
	if _, err := a.RenamePlaylist(ctx, p, pl.ID, " "); !isKind(err, domain.ErrInvalid) {
		t.Errorf("empty name: %v", err)
	}

	// Playlists belong to the profile: another profile does not see them.
	other, err := a.CreateProfile(ctx, p, "Other", "", false, nil, "")
	mustNil(t, err)
	onOther := p
	onOther.Profile = &other
	if lists, _ := a.Playlists(ctx, onOther); len(lists) != 0 {
		t.Errorf("another profile's playlists: %+v", lists)
	}
	if _, _, err := a.PlaylistWithEntries(ctx, onOther, pl.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("another profile's playlist: %v", err)
	}
	if _, err := a.AddToPlaylist(ctx, p, pl.ID, []domain.ID{domain.NewID()}, -1); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown item: %v", err)
	}
	mustNil(t, a.DeletePlaylist(ctx, p, pl.ID))
	if lists, _ := a.Playlists(ctx, p); len(lists) != 0 {
		t.Errorf("after deletion: %+v", lists)
	}
}
