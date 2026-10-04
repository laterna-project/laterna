package app

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/metadata"
	"github.com/laterna-project/laterna/internal/store"
)

// Collections: franchises read from NFO files (<set>, refreshed every time metadata is read) or
// selections made by an administrator. They are shared by the whole server and filtered like the
// rest of the catalog: a profile only sees the collections it can see an item of.

const (
	maxCollectionName     = 100
	maxCollectionOverview = 4000
)

// nfoSet converts the collection an NFO names: the key is the collection's TMDb ID if known,
// otherwise its normalized name.
func nfoSet(s *metadata.Set) *store.NFOSet {
	if s == nil {
		return nil
	}
	key := "name:" + store.NameKey(s.Name)
	if s.TMDbID != "" {
		key = "tmdb:" + s.TMDbID
	}
	return &store.NFOSet{Key: key, Name: s.Name, Overview: s.Overview}
}

// Collections lists the collections the caller can see in a library (all of them if libraryID is
// nil). An administrator also sees manual collections that are still empty.
func (a *App) Collections(ctx context.Context, p domain.Principal, libraryID *domain.ID) ([]domain.CollectionView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return nil, err
	}
	if libraryID != nil && !v.AllowsLibrary(*libraryID) {
		return nil, domain.NotFound("library.not_found")
	}
	return a.store.Read().Collections(ctx, v, libraryID, p.CanAdminister())
}

// Collection returns a collection and its visible movies and series, by release date.
func (a *App) Collection(ctx context.Context, p domain.Principal, id domain.ID) (domain.CollectionView, []domain.ItemView, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.CollectionView{}, nil, err
	}
	read := a.store.Read()
	c, err := read.Collection(ctx, id)
	if store.IsNotFound(err) {
		return domain.CollectionView{}, nil, domain.NotFound("collection.not_found")
	}
	if err != nil {
		return domain.CollectionView{}, nil, err
	}
	items, err := read.CollectionItems(ctx, v, id, 0)
	if err != nil {
		return domain.CollectionView{}, nil, err
	}
	if len(items) == 0 && (!c.Manual || !p.CanAdminister()) {
		return domain.CollectionView{}, nil, domain.NotFound("collection.not_found")
	}
	view := domain.CollectionView{Collection: c, ItemCount: len(items)}
	view.Images = collectionPosters(items)
	return view, items, nil
}

func collectionPosters(items []domain.ItemView) []domain.Image {
	var out []domain.Image
	for _, it := range items {
		for _, img := range it.Images {
			if img.Kind == domain.ImagePoster {
				out = append(out, img)
				break
			}
		}
		if len(out) == 4 {
			break
		}
	}
	return out
}

// CreateCollection creates a manual collection, with movies and series.
func (a *App) CreateCollection(ctx context.Context, name, overview string, itemIDs []domain.ID) (domain.Collection, error) {
	name, overview = strings.TrimSpace(name), strings.TrimSpace(overview)
	if err := validateCollection(name, overview); err != nil {
		return domain.Collection{}, err
	}
	now := a.now()
	c := domain.Collection{ID: domain.NewID(), Name: name, Overview: overview, Manual: true, CreatedAt: now, UpdatedAt: now}
	err := a.store.Write(ctx, func(q store.Q) error {
		if err := q.CreateCollection(ctx, c); err != nil {
			return err
		}
		return a.addToCollection(ctx, q, c.ID, itemIDs)
	})
	return c, err
}

// CollectionChanges describes a change to a collection; a nil field is left alone.
type CollectionChanges struct {
	Name, Overview *string
	// Add and Remove list the movies and series to add and to remove.
	Add, Remove []domain.ID
}

// UpdateCollection changes a manual collection (NFO-based ones follow their NFO files).
func (a *App) UpdateCollection(ctx context.Context, id domain.ID, ch CollectionChanges) (domain.Collection, error) {
	var c domain.Collection
	err := a.store.Write(ctx, func(q store.Q) error {
		var err error
		c, err = q.Collection(ctx, id)
		if store.IsNotFound(err) {
			return domain.NotFound("collection.not_found")
		}
		if err != nil {
			return err
		}
		if !c.Manual {
			return domain.Precondition("collection.from_nfo")
		}
		if ch.Name != nil {
			c.Name = strings.TrimSpace(*ch.Name)
		}
		if ch.Overview != nil {
			c.Overview = strings.TrimSpace(*ch.Overview)
		}
		if err := validateCollection(c.Name, c.Overview); err != nil {
			return err
		}
		c.UpdatedAt = a.now()
		if err := q.UpdateCollection(ctx, c); err != nil {
			return err
		}
		if err := a.addToCollection(ctx, q, c.ID, ch.Add); err != nil {
			return err
		}
		for _, item := range ch.Remove {
			if err := q.RemoveCollectionItem(ctx, c.ID, item); err != nil {
				return err
			}
		}
		return nil
	})
	return c, err
}

// DeleteCollection deletes a manual collection (never its movies and series).
func (a *App) DeleteCollection(ctx context.Context, id domain.ID) error {
	return a.store.Write(ctx, func(q store.Q) error {
		c, err := q.Collection(ctx, id)
		if store.IsNotFound(err) {
			return domain.NotFound("collection.not_found")
		}
		if err != nil {
			return err
		}
		if !c.Manual {
			return domain.Precondition("collection.from_nfo")
		}
		return q.DeleteCollection(ctx, id)
	})
}

// addToCollection adds existing movies and series to a collection.
func (a *App) addToCollection(ctx context.Context, q store.Q, collectionID domain.ID, itemIDs []domain.ID) error {
	now := a.now()
	for _, id := range itemIDs {
		it, err := q.Item(ctx, id)
		if store.IsNotFound(err) || (err == nil && it.Kind != domain.ItemMovie && it.Kind != domain.ItemSeries) {
			return domain.Invalid("catalog.item_not_found", "item_id", id)
		}
		if err != nil {
			return err
		}
		if err := q.AddCollectionItem(ctx, collectionID, id, now); err != nil {
			return err
		}
	}
	return nil
}

func validateCollection(name, overview string) error {
	switch {
	case name == "":
		return domain.Invalid("collection.name_required")
	case utf8.RuneCountInString(name) > maxCollectionName:
		return domain.Invalid("collection.name_too_long", "max", maxCollectionName)
	case strings.ContainsFunc(name, unicode.IsControl):
		return domain.Invalid("collection.name_invalid")
	case utf8.RuneCountInString(overview) > maxCollectionOverview:
		return domain.Invalid("collection.overview_too_long", "max", maxCollectionOverview)
	}
	return nil
}
