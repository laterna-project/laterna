package recommend

import (
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// A small catalog: two movies from the same franchise and director, one that only shares the genre,
// and an unrelated one.
func catalog() (items []domain.ItemFeatures, ids map[string]domain.ID) {
	ids = map[string]domain.ID{}
	for _, n := range []string{"saga1", "saga2", "drame", "comedie", "nolan", "saga", "acteur"} {
		ids[n] = domain.NewID()
	}
	common := []string{"Drame"}
	items = []domain.ItemFeatures{
		{
			ID: ids["saga1"], Kind: domain.ItemMovie, Year: 2010, Genres: []string{"Science-fiction", "Action"}, Collections: []domain.ID{ids["saga"]},
			People: []domain.FeaturePerson{{ID: ids["nolan"], Role: domain.RoleDirector}, {ID: ids["acteur"], Role: domain.RoleActor}},
		},
		{
			ID: ids["saga2"], Kind: domain.ItemMovie, Year: 2014, Genres: []string{"Science-fiction", "action"}, Collections: []domain.ID{ids["saga"]},
			People: []domain.FeaturePerson{{ID: ids["nolan"], Role: domain.RoleDirector}}, Rating: 8,
		},
		{ID: ids["drame"], Kind: domain.ItemMovie, Year: 2011, Genres: append(common, "Science-fiction")},
		{ID: ids["comedie"], Kind: domain.ItemSeries, Year: 1995, Genres: []string{"Comédie"}, Studios: []string{"Autre"}},
	}
	// Filler: "Drame" is common, so it says little.
	for range 20 {
		items = append(items, domain.ItemFeatures{ID: domain.NewID(), Kind: domain.ItemMovie, Genres: common})
	}
	return items, ids
}

func TestSimilar(t *testing.T) {
	items, ids := catalog()
	x := Build(items)
	got := x.Similar(ids["saga1"], nil, 3)
	if len(got) < 2 || got[0].ID != ids["saga2"] || got[1].ID != ids["drame"] {
		t.Fatalf("close to saga1: %+v", got)
	}
	for _, s := range got {
		if s.ID == ids["saga1"] || s.ID == ids["comedie"] {
			t.Errorf("unexpected neighbor: %+v", s)
		}
	}
	// skip leaves an item out.
	if got := x.Similar(ids["saga1"], func(id domain.ID) bool { return id == ids["saga2"] }, 3); len(got) == 0 || got[0].ID != ids["drame"] {
		t.Errorf("with skip: %+v", got)
	}
	if got := x.Similar(domain.NewID(), nil, 3); got != nil {
		t.Errorf("unknown item: %+v", got)
	}
}

func TestRecommend(t *testing.T) {
	items, ids := catalog()
	x := Build(items)
	now := time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC)
	taste := Taste([]domain.TasteSignal{{ID: ids["saga1"], Watched: 2 * time.Hour, Played: true, LastAt: now.Add(-24 * time.Hour)}}, now)
	seen := func(id domain.ID) bool { return id == ids["saga1"] }
	got := x.Recommend(taste, seen, 2)
	if len(got) != 2 || got[0].ID != ids["saga2"] || got[1].ID != ids["drame"] {
		t.Fatalf("recommendations: %+v", got)
	}
	if got := x.Recommend(map[domain.ID]float64{}, seen, 2); got != nil {
		t.Errorf("no taste: %+v", got)
	}
}

func TestTaste(t *testing.T) {
	now := time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC)
	a, b, c := domain.NewID(), domain.NewID(), domain.NewID()
	w := Taste([]domain.TasteSignal{
		{ID: a, Watched: 3 * time.Hour, LastAt: now},
		{ID: b, Watched: 3 * time.Hour, LastAt: now.Add(-halfLife)}, // six months earlier: half
		{ID: c, Favorite: true},                                     // no date: half
	}, now)
	if d := w[b] / w[a]; d < 0.49 || d > 0.51 {
		t.Errorf("decay: %v / %v", w[b], w[a])
	}
	if w[c] != 1 {
		t.Errorf("favorite without a date: %v", w[c])
	}
}
