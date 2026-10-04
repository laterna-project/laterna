package app

import (
	"context"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/store"
)

// Recommendations on the fixture movies, with extra metadata: "HDR Test" shares its franchise,
// director and genres with "Big Test Movie"; "Sans Index" shares a genre and an actor; "Versions"
// and "Deux Pistes" share a genre. A movie already played is never recommended.
func TestRecommendations(t *testing.T) {
	a, _, p, movies := moviesByTitle(t)
	ctx := context.Background()
	credit := func(name string, role domain.PersonRole) domain.Credit { return domain.Credit{Name: name, Role: role} }
	meta := map[string]domain.Metadata{
		"Big Test Movie": {Genres: []string{"Action", "Science-fiction"}, Credits: []domain.Credit{credit("Réal A", domain.RoleDirector), credit("Acteur X", domain.RoleActor)}},
		"HDR Test":       {Genres: []string{"Science-fiction", "Action"}, Credits: []domain.Credit{credit("Réal A", domain.RoleDirector)}},
		"Sans Index":     {Genres: []string{"Action"}, Credits: []domain.Credit{credit("Acteur X", domain.RoleActor)}},
		"Versions":       {Genres: []string{"Action"}},
		"Deux Pistes":    {Genres: []string{"Action"}},
	}
	mustNil(t, a.store.Write(ctx, func(q store.Q) error {
		for title, m := range meta {
			it := movies[title]
			m.Title, m.SortTitle, m.Year = it.Title, it.SortTitle, it.Year
			if _, err := q.SetMetadata(ctx, it.ID, m, a.now()); err != nil {
				return err
			}
		}
		saga := &store.NFOSet{Key: "saga", Name: "Saga"}
		if err := q.SetNFOCollection(ctx, movies["Big Test Movie"].ID, saga, a.now()); err != nil {
			return err
		}
		return q.SetNFOCollection(ctx, movies["HDR Test"].ID, saga, a.now())
	}))
	a.catalogChanged()

	titles := func(views []domain.ItemView) []string {
		var out []string
		for _, v := range views {
			out = append(out, v.Item.Title)
		}
		return out
	}
	similar, err := a.Similar(ctx, p, movies["Big Test Movie"].ID, 0)
	mustNil(t, err)
	if got := titles(similar); len(got) != 4 || got[0] != "HDR Test" || got[1] != "Sans Index" {
		t.Errorf("close titles: %v", got)
	}

	// Nothing watched yet: no recommendation.
	rows, err := a.Home(ctx, p, 0)
	mustNil(t, err)
	if slices.ContainsFunc(rows, func(r HomeRow) bool { return r.Kind == RowRecommended }) {
		t.Errorf("recommendations without a taste: %+v", rows)
	}
	mustNil(t, a.SetPlayed(ctx, p, movies["Big Test Movie"].ID, true))
	rows, err = a.Home(ctx, p, 1)
	mustNil(t, err)
	var rec, because *HomeRow
	for i := range rows {
		switch rows[i].Kind {
		case RowRecommended:
			rec = &rows[i]
		case RowBecauseYouWatched:
			because = &rows[i]
		case RowResume, RowNextUp, RowRecentAlbums, RowLatestMovies, RowLatestSeries, RowLatestAlbums, RowReading,
			RowLatestBooks, RowLatestPhotos:
		}
	}
	// The "Because" row takes the title closest to its source, "Recommended" the next one, and no
	// title is repeated.
	if because == nil || because.Source == nil || *because.Source != movies["Big Test Movie"].ID ||
		because.Title.String() != "home.because_you_watched (title=Big Test Movie)" || titles(because.Items)[0] != "HDR Test" {
		t.Fatalf("because: %+v", because)
	}
	if i := slices.IndexFunc(rows, func(r HomeRow) bool { return r.Kind == RowRecommended }); rec == nil || len(rec.Items) == 0 ||
		titles(rec.Items)[0] != "Sans Index" || i+1 >= len(rows) || rows[i+1].Kind != RowBecauseYouWatched {
		t.Errorf("recommended: %+v", rec)
	}
	for _, r := range rows {
		if slices.Contains(titles(r.Items), "Big Test Movie") && (r.Kind == RowRecommended || r.Kind == RowBecauseYouWatched) {
			t.Errorf("played movie recommended in %s", r.Kind)
		}
	}
	if _, err := a.Similar(ctx, p, domain.NewID(), 0); !isKind(err, domain.ErrNotFound) {
		t.Errorf("unknown item: %v", err)
	}
}
