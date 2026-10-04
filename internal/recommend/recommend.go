// Package recommend recommends movies and series without any outside source: it only uses what
// describes them (genres, directors, writers, top-billed actors, studios, collection, decade) and
// what the profile has watched. Pure logic: the catalog and the profile's signals are passed in.
//
// Each item is a feature vector. A feature weighs according to its kind (a collection counts more
// than a genre) and its rarity (idf: a genre everything has says next to nothing). Two items are as
// close as the cosine of their vectors. A profile's taste is the sum of the vectors of what it
// watched, weighted by how much it engaged.
package recommend

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Weight of each kind of feature, before rarity.
const (
	weightCollection = 3
	weightDirector   = 2
	weightWriter     = 1.5
	weightActor      = 1
	weightGenre      = 1
	weightStudio     = 0.5
	weightDecade     = 0.3
)

// halfLife: something watched counts half as much in the profile's taste every six months.
const halfLife = 180 * 24 * time.Hour

// Scored is an item and how close it is (roughly 0 to 1).
type Scored struct {
	ID    domain.ID
	Score float64
}

// Index is the vectorized catalog.
type Index struct {
	ids    []domain.ID
	pos    map[domain.ID]int
	vec    [][]feat // sorted by key, normalized
	rating []float64
	keys   int // number of distinct features
}

type feat struct {
	key int32
	w   float64
}

// Build vectorizes the catalog.
func Build(items []domain.ItemFeatures) *Index {
	keys := map[string]int32{}
	df := map[int32]int{}
	raw := make([]map[int32]float64, len(items))
	x := &Index{ids: make([]domain.ID, len(items)), pos: make(map[domain.ID]int, len(items)), rating: make([]float64, len(items))}
	for i, it := range items {
		x.ids[i], x.pos[it.ID], x.rating[i] = it.ID, i, it.Rating
		m := map[int32]float64{}
		add := func(name string, w float64) {
			k, ok := keys[name]
			if !ok {
				k = int32(len(keys)) //nolint:gosec // fewer than 2 billion features
				keys[name] = k
			}
			if _, seen := m[k]; !seen {
				df[k]++
			}
			m[k] = max(m[k], w)
		}
		for _, g := range it.Genres {
			add("g:"+strings.ToLower(strings.TrimSpace(g)), weightGenre)
		}
		for _, s := range it.Studios {
			add("s:"+strings.ToLower(strings.TrimSpace(s)), weightStudio)
		}
		for _, c := range it.Collections {
			add("c:"+c.String(), weightCollection)
		}
		for _, p := range it.People {
			switch p.Role {
			case domain.RoleDirector:
				add("d:"+p.ID.String(), weightDirector)
			case domain.RoleWriter:
				add("w:"+p.ID.String(), weightWriter)
			case domain.RoleActor:
				add("a:"+p.ID.String(), weightActor)
			case domain.RoleIllustrator: // books only
			}
		}
		if it.Year > 0 {
			add("y:"+strconv.Itoa(it.Year/10), weightDecade)
		}
		raw[i] = m
	}
	n := float64(len(items))
	x.keys = len(keys)
	x.vec = make([][]feat, len(items))
	for i, m := range raw {
		v := make([]feat, 0, len(m))
		var norm float64
		for k, w := range m {
			w *= math.Log(1 + n/float64(df[k]))
			v = append(v, feat{key: k, w: w})
			norm += w * w
		}
		if norm > 0 {
			norm = math.Sqrt(norm)
			for j := range v {
				v[j].w /= norm
			}
		}
		slices.SortFunc(v, func(a, b feat) int { return cmp.Compare(a.key, b.key) })
		x.vec[i] = v
	}
	return x
}

// Len is the number of items in the catalog.
func (x *Index) Len() int { return len(x.ids) }

// Similar returns the items closest to id, closest first, limit at most. id itself and whatever
// skip rejects are left out.
func (x *Index) Similar(id domain.ID, skip func(domain.ID) bool, limit int) []Scored {
	i, ok := x.pos[id]
	if !ok {
		return nil
	}
	return x.rank(func(j int) float64 { return dot(x.vec[i], x.vec[j]) }, func(j int) bool {
		return j == i || skip != nil && skip(x.ids[j])
	}, limit)
}

// Recommend returns the items closest to the profile's taste (a weight per item, see Taste),
// closest first, limit at most, without whatever skip rejects. A good rating barely breaks ties
// between items that are equally close.
func (x *Index) Recommend(taste map[domain.ID]float64, skip func(domain.ID) bool, limit int) []Scored {
	profile := make([]float64, x.keys)
	for id, w := range taste {
		if i, ok := x.pos[id]; ok && w > 0 {
			for _, f := range x.vec[i] {
				profile[f.key] += w * f.w
			}
		}
	}
	var norm float64
	for _, w := range profile {
		norm += w * w
	}
	if norm == 0 {
		return nil
	}
	norm = math.Sqrt(norm)
	return x.rank(func(j int) float64 {
		var s float64
		for _, f := range x.vec[j] {
			s += profile[f.key] * f.w
		}
		return s/norm + 0.01*x.rating[j]/10
	}, func(j int) bool { return skip != nil && skip(x.ids[j]) }, limit)
}

func (x *Index) rank(score func(int) float64, skip func(int) bool, limit int) []Scored {
	var out []Scored
	for j := range x.ids {
		if skip(j) {
			continue
		}
		if s := score(j); s > 0.01 {
			out = append(out, Scored{ID: x.ids[j], Score: s})
		}
	}
	slices.SortFunc(out, func(a, b Scored) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), slices.Compare(a.ID[:], b.ID[:]))
	})
	return out[:min(limit, len(out))]
}

// dot is the dot product of two vectors sorted by key.
func dot(a, b []feat) float64 {
	var s float64
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i].key < b[j].key:
			i++
		case a[i].key > b[j].key:
			j++
		default:
			s += a[i].w * b[j].w
			i++
			j++
		}
	}
	return s
}

// Taste turns a profile's signals into a weight per item: time watched (in hours, dampened),
// episodes played, played, favorite. The whole thing is halved every six months since the last time
// it was watched; a signal without a date counts for half.
func Taste(signals []domain.TasteSignal, now time.Time) map[domain.ID]float64 {
	out := make(map[domain.ID]float64, len(signals))
	for _, s := range signals {
		w := math.Log1p(s.Watched.Hours()) + 0.5*math.Log1p(float64(s.Episodes))
		if s.Played {
			w++
		}
		if s.Favorite {
			w += 2
		}
		decay := 0.5
		if !s.LastAt.IsZero() {
			decay = math.Pow(0.5, max(0, now.Sub(s.LastAt).Hours())/halfLife.Hours())
		}
		if w *= decay; w > 0 {
			out[s.ID] += w
		}
	}
	return out
}
