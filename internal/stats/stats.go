// Package stats computes a profile's statistics and yearly recap from its history. Pure logic: the
// plays, their genres and the client's time zone are passed in. Plays come one at a time (Acc.Add),
// so a year of history is never held in memory.
package stats

import (
	"cmp"
	"slices"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// TopSize is the number of lines in each ranking.
const TopSize = 5

// Bounds returns the start and the (excluded) end of calendar year year in loc. Both are zero for
// year = 0 (all time).
func Bounds(year int, loc *time.Location) (from, to time.Time) {
	if year == 0 {
		return time.Time{}, time.Time{}
	}
	return time.Date(year, time.January, 1, 0, 0, 0, 0, loc), time.Date(year+1, time.January, 1, 0, 0, 0, 0, loc)
}

// Acc accumulates the plays of a period: calendar year year (0 for all time) in time zone loc. Each
// play counts for the day, hour and month it started.
type Acc struct {
	year     int
	loc      *time.Location
	from, to time.Time
	st       domain.Stats

	series, movies, artists, tracks      ranking
	seenMovies, seenEpisodes, seenTracks map[itemKey]bool
	days                                 map[int]time.Duration // year * 10000 + month * 100 + day
	months                               map[int]time.Duration // year * 100 + month
	binges                               map[bingeKey]bingeAcc
	bingeEpisodes                        map[bingeEpisode]bool
	owners                               map[domain.ID]ownerAcc // movies and series, for their genres
}

// New starts accumulating a period.
func New(year int, loc *time.Location) *Acc {
	a := &Acc{
		year: year, loc: loc,
		series: ranking{}, movies: ranking{}, artists: ranking{}, tracks: ranking{},
		seenMovies: map[itemKey]bool{}, seenEpisodes: map[itemKey]bool{}, seenTracks: map[itemKey]bool{},
		days: map[int]time.Duration{}, months: map[int]time.Duration{},
		binges: map[bingeKey]bingeAcc{}, bingeEpisodes: map[bingeEpisode]bool{}, owners: map[domain.ID]ownerAcc{},
	}
	a.from, a.to = Bounds(year, loc)
	return a
}

// Compute sums up plays in one go. genres gives the genres of a movie or a series (an episode has
// those of its series).
func Compute(plays []domain.Play, genres map[domain.ID][]string, year int, loc *time.Location) domain.Stats {
	a := New(year, loc)
	for i := range plays {
		a.Add(&plays[i])
	}
	return a.Result(genres)
}

// Add counts a play; one that starts outside the period is ignored. p is not kept.
func (a *Acc) Add(p *domain.Play) {
	if a.year != 0 && (p.StartedAt.Before(a.from) || !p.StartedAt.Before(a.to)) {
		return
	}
	// Local time is worked out once: the zone offset at that instant, then a shifted UTC time. This
	// avoids any further lookup in the zone rules.
	_, offset := p.StartedAt.In(a.loc).Zone()
	start := p.StartedAt.Add(time.Duration(offset) * time.Second).UTC()
	w := p.Watched
	st := &a.st
	st.Plays++
	st.Total += w
	y, m, d := start.Date()
	day := y*10000 + int(m)*100 + d
	a.days[day] += w
	a.months[y*100+int(m)] += w
	st.ByHour[(int(start.Weekday())+6)%7*24+start.Hour()] += w
	switch p.Kind {
	case domain.ItemMovie:
		st.MoviesTime += w
		a.seenMovies[key(p.ItemID, "", p.Title)] = true
		a.movies.add(p.ItemID, "", p.Title, w)
		a.owner(p.ItemID, w)
	case domain.ItemEpisode:
		st.EpisodesTime += w
		a.seenEpisodes[key(p.ItemID, p.Subtitle, p.Title)] = true
		a.series.add(p.SeriesID, "", p.Subtitle, w)
		a.owner(p.SeriesID, w)
		k := bingeKey{day: day, series: key(p.SeriesID, "", p.Subtitle)}
		b, ok := a.binges[k]
		if !ok {
			b = bingeAcc{Binge: domain.Binge{SeriesID: p.SeriesID, Series: p.Subtitle}, day: day}
		}
		if ep := (bingeEpisode{bingeKey: k, episode: key(p.ItemID, "", p.Title)}); !a.bingeEpisodes[ep] {
			a.bingeEpisodes[ep] = true
			b.Episodes++
		}
		b.Time += w
		a.binges[k] = b
	case domain.ItemTrack:
		st.MusicTime += w
		a.seenTracks[key(p.ItemID, p.Subtitle, p.Title)] = true
		a.artists.add(p.ArtistID, "", p.Subtitle, w)
		a.tracks.add(p.ItemID, p.Subtitle, p.Title, w)
	case domain.ItemSeries, domain.ItemSeason, domain.ItemArtist, domain.ItemAlbum, domain.ItemBookSeries,
		domain.ItemBook, domain.ItemPhotoAlbum, domain.ItemPhoto:
	}
}

// Owners returns the movies and series watched, the ones we need genres for.
func (a *Acc) Owners() []domain.ID {
	out := make([]domain.ID, 0, len(a.owners))
	for id := range a.owners {
		out = append(out, id)
	}
	return out
}

// Result returns the summary. genres gives the genres of the movies and series (Owners), and each
// genre gets the time of its movies and series. First and Last are left for the caller to fill with
// complete plays.
func (a *Acc) Result(genres map[domain.ID][]string) domain.Stats {
	st := a.st
	genre := ranking{}
	for id, o := range a.owners {
		for _, g := range genres[id] {
			e := genre.entry(nil, "", g)
			e.Time += o.time
			e.Plays += o.plays
		}
	}
	st.Movies, st.Episodes, st.Tracks, st.Series = len(a.seenMovies), len(a.seenEpisodes), len(a.seenTracks), len(a.series)
	byTime := func(a, b domain.StatEntry) int {
		return cmp.Or(cmp.Compare(b.Time, a.Time), cmp.Compare(b.Plays, a.Plays), cmp.Compare(a.Name, b.Name))
	}
	byPlays := func(a, b domain.StatEntry) int {
		return cmp.Or(cmp.Compare(b.Plays, a.Plays), cmp.Compare(b.Time, a.Time), cmp.Compare(a.Name, b.Name))
	}
	st.TopSeries, st.TopMovies, st.TopArtists = a.series.top(byTime), a.movies.top(byTime), a.artists.top(byTime)
	st.TopTracks, st.TopGenres = a.tracks.top(byPlays), genre.top(byTime)
	st.Timeline = timeline(a.months, a.year, a.loc)
	busiest := 0
	for day, t := range a.days {
		if busiest == 0 || t > a.days[busiest] || t == a.days[busiest] && day < busiest {
			busiest = day
		}
	}
	if busiest != 0 {
		st.BusiestDay = &domain.TimeBucket{Start: dayStart(busiest, a.loc), Time: a.days[busiest]}
	}
	var best bingeAcc
	for _, b := range a.binges {
		if b.Episodes > best.Episodes || b.Episodes == best.Episodes && (b.Time > best.Time || b.Time == best.Time && b.day < best.day) {
			best = b
		}
	}
	if best.Episodes > 0 {
		binge := best.Binge
		binge.Day = dayStart(best.day, a.loc)
		st.Binge = &binge
	}
	return st
}

func (a *Acc) owner(id *domain.ID, w time.Duration) {
	if id == nil {
		return
	}
	o := a.owners[*id]
	o.time += w
	o.plays++
	a.owners[*id] = o
}

type ownerAcc struct {
	time  time.Duration
	plays int
}

// dayStart is midnight of the day written as year * 10000 + month * 100 + day.
func dayStart(day int, loc *time.Location) time.Time {
	return time.Date(day/10000, time.Month(day/100%100), day%100, 0, 0, 0, 0, loc)
}

// timeline returns the months of a year (all twelve), or the years since the first play. months is
// keyed by year * 100 + month.
func timeline(months map[int]time.Duration, year int, loc *time.Location) []domain.TimeBucket {
	if year != 0 {
		out := make([]domain.TimeBucket, 12)
		for m := range out {
			out[m] = domain.TimeBucket{Start: time.Date(year, time.Month(m+1), 1, 0, 0, 0, 0, loc), Time: months[year*100+m+1]}
		}
		return out
	}
	years := map[int]time.Duration{}
	for m, t := range months {
		years[m/100] += t
	}
	var out []domain.TimeBucket
	for y, t := range years {
		out = append(out, domain.TimeBucket{Start: time.Date(y, time.January, 1, 0, 0, 0, 0, loc), Time: t})
	}
	slices.SortFunc(out, func(a, b domain.TimeBucket) int { return a.Start.Compare(b.Start) })
	return out
}

// itemKey identifies an item: its ID, or for a forgotten item its name prefixed with its series or
// artists.
type itemKey struct {
	id           domain.ID
	parent, name string
}

func key(id *domain.ID, parent, name string) itemKey {
	if id != nil {
		return itemKey{id: *id}
	}
	return itemKey{parent: parent, name: name}
}

type ranking map[itemKey]*domain.StatEntry

// entry returns the line of an item, creating it if needed. A track is shown with its artists
// (parent).
func (r ranking) entry(id *domain.ID, parent, name string) *domain.StatEntry {
	k := key(id, parent, name)
	e := r[k]
	if e == nil {
		display := name
		if parent != "" {
			display = parent + " — " + name
		}
		e = &domain.StatEntry{ID: id, Name: display}
		r[k] = e
	}
	return e
}

func (r ranking) add(id *domain.ID, parent, name string, w time.Duration) {
	e := r.entry(id, parent, name)
	e.Time += w
	e.Plays++
}

func (r ranking) top(order func(a, b domain.StatEntry) int) []domain.StatEntry {
	out := make([]domain.StatEntry, 0, len(r))
	for _, e := range r {
		out = append(out, *e)
	}
	slices.SortFunc(out, order)
	return out[:min(TopSize, len(out))]
}

type bingeKey struct {
	day    int
	series itemKey
}

type bingeAcc struct {
	domain.Binge
	day int
}

// bingeEpisode is an episode watched on a given day, so that a rewatched episode counts once.
type bingeEpisode struct {
	bingeKey
	episode itemKey
}
