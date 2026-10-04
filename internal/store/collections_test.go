package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func collectionNames(cs []domain.CollectionView) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func TestCollections(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	read := f.st.Read()
	alien, brazil, amelie := f.movies["Alien"], f.movies["Brazil"], f.movies["Amélie"]

	// NFO collections: created, extended, renamed, emptied and then forgotten.
	set := &NFOSet{Key: "tmdb:8091", Name: "Alien - La saga"}
	mustWrite(t, f.st, func(q Q) error {
		if err := q.SetNFOCollection(ctx, brazil.ID, set, t0); err != nil {
			return err
		}
		return q.SetNFOCollection(ctx, alien.ID, set, t0)
	})
	cs, err := read.Collections(ctx, f.viewer(), nil, false)
	if err != nil || len(cs) != 1 || cs[0].ItemCount != 2 || cs[0].Manual {
		t.Fatalf("collections: %+v %v", cs, err)
	}
	items, _ := read.CollectionItems(ctx, f.viewer(), cs[0].ID, 0)
	if len(items) != 2 || items[0].Item.ID != alien.ID {
		t.Errorf("by release date: %v", items)
	}
	mustWrite(t, f.st, func(q Q) error {
		return q.SetNFOCollection(ctx, alien.ID, &NFOSet{Key: "tmdb:8091", Name: "Alien"}, t0)
	})
	if c, _ := read.Collection(ctx, cs[0].ID); c.Name != "Alien" {
		t.Errorf("renamed: %+v", c)
	}
	if in, _ := read.ItemCollections(ctx, alien.ID); len(in) != 1 || in[0].Name != "Alien" {
		t.Errorf("Alien's collections: %+v", in)
	}
	mustWrite(t, f.st, func(q Q) error {
		return errors.Join(q.SetNFOCollection(ctx, alien.ID, nil, t0), q.SetNFOCollection(ctx, brazil.ID, nil, t0))
	})
	if _, err := read.Collection(ctx, cs[0].ID); !IsNotFound(err) {
		t.Errorf("empty collection kept: %v", err)
	}

	// Manual collection: filtered by age; when empty, only shown to administrators.
	manual := domain.Collection{ID: domain.NewID(), Name: "Favoris de Chloé", CreatedAt: t0, UpdatedAt: t0}
	ten, twelve := 10, 12
	mustWrite(t, f.st, func(q Q) error {
		_, err := q.SetMetadata(ctx, amelie.ID, domain.Metadata{Title: amelie.Title, SortTitle: amelie.SortTitle, AgeRating: &twelve}, t0)
		return errors.Join(err, q.CreateCollection(ctx, manual), q.AddCollectionItem(ctx, manual.ID, amelie.ID, t0), q.AddCollectionItem(ctx, manual.ID, f.series.ID, t0))
	})
	cs, _ = read.Collections(ctx, f.viewer(), nil, false)
	if len(cs) != 1 || !cs[0].Manual || cs[0].ItemCount != 2 {
		t.Errorf("manual collection: %+v", cs)
	}
	kid := domain.Viewer{ProfileID: f.profile, Parental: domain.ParentalControl{MaxAge: &ten, BlockUnrated: true}}
	if cs, _ := read.Collections(ctx, kid, nil, false); len(cs) != 0 {
		t.Errorf("nothing visible for a kid: %+v", cs)
	}
	if cs, _ := read.Collections(ctx, kid, nil, true); len(cs) != 1 || cs[0].ItemCount != 0 {
		t.Errorf("empty but manageable: %+v", cs)
	}
	other := domain.NewID()
	if cs, _ := read.Collections(ctx, f.viewer(), &other, false); len(cs) != 0 {
		t.Errorf("other library: %v", collectionNames(cs))
	}
	mustWrite(t, f.st, func(q Q) error { return q.RemoveCollectionItem(ctx, manual.ID, amelie.ID) })
	if items, _ := read.CollectionItems(ctx, f.viewer(), manual.ID, 0); len(items) != 1 || items[0].Item.ID != f.series.ID {
		t.Errorf("after removal: %v", items)
	}
}

func TestPlaylists(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	read := f.st.Read()
	amelie, ep := f.movies["Amélie"], f.eps[0]
	p := domain.Playlist{ID: domain.NewID(), ProfileID: f.profile, Name: "Soirée", CreatedAt: t0, UpdatedAt: t0}
	refs := []PlaylistEntryRef{{ID: domain.NewID(), ItemID: amelie.ID}, {ID: domain.NewID(), ItemID: ep.ID}, {ID: domain.NewID(), ItemID: amelie.ID}}
	mustWrite(t, f.st, func(q Q) error {
		_, err := q.SetMetadata(ctx, amelie.ID, domain.Metadata{Title: amelie.Title, SortTitle: amelie.SortTitle, Runtime: 2 * time.Hour}, t0)
		return errors.Join(err, q.CreatePlaylist(ctx, p), q.SetPlaylistEntries(ctx, p.ID, refs, t0))
	})
	entries, err := read.PlaylistEntries(ctx, f.viewer(), p.ID, 0)
	if err != nil || len(entries) != 3 || entries[0].Item.Item.ID != amelie.ID || entries[1].Item.Episode == nil || entries[2].ID != refs[2].ID {
		t.Fatalf("entries: %+v %v", entries, err)
	}
	views, err := read.Playlists(ctx, f.viewer())
	if err != nil || len(views) != 1 || views[0].EntryCount != 3 || views[0].Duration < 4*time.Hour {
		t.Errorf("playlists: %+v %v", views, err)
	}
	// Reordered, one entry removed.
	mustWrite(t, f.st, func(q Q) error { return q.SetPlaylistEntries(ctx, p.ID, []PlaylistEntryRef{refs[1], refs[0]}, t0) })
	if got, _ := read.PlaylistEntryRefs(ctx, p.ID); !slices.Equal(got, []PlaylistEntryRef{refs[1], refs[0]}) {
		t.Errorf("after reordering: %+v", got)
	}
	// What is not visible does not show up.
	none := domain.Viewer{ProfileID: f.profile, Libraries: []domain.ID{}}
	if e, _ := read.PlaylistEntries(ctx, none, p.ID, 0); len(e) != 0 {
		t.Errorf("no library: %+v", e)
	}
	if v, _ := read.PlaylistView(ctx, none, p); v.EntryCount != 0 {
		t.Errorf("count with no library: %+v", v)
	}
	mustWrite(t, f.st, func(q Q) error { return q.DeletePlaylist(ctx, p.ID) })
	if n, _ := read.CountPlaylists(ctx, f.profile); n != 0 {
		t.Errorf("%d playlists left", n)
	}
}
