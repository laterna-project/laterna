package stats

import (
	"testing"
	"time"
	_ "time/tzdata" // time zones without relying on the local Go install (the test binary is built with -trimpath)

	"github.com/laterna-project/laterna/internal/domain"
)

func id() *domain.ID {
	v := domain.NewID()
	return &v
}

func TestCompute(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, paris)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	show, film, artist := id(), id(), id()
	ep1, ep2, ep3, track := id(), id(), id(), id()
	genres := map[domain.ID][]string{*show: {"Drame", "Anime"}, *film: {"Drame"}}
	episode := func(item *domain.ID, title, start string) domain.Play {
		return domain.Play{ItemID: item, Kind: domain.ItemEpisode, SeriesID: show, Title: title, Subtitle: "Série", StartedAt: at(start), Watched: 24 * time.Minute}
	}
	plays := []domain.Play{
		// March 14: three episodes of the series, one of them rewatched, then a movie in the
		// evening.
		episode(ep1, "Un", "2026-03-14 14:00"),
		episode(ep2, "Deux", "2026-03-14 14:30"),
		episode(ep2, "Deux", "2026-03-14 15:00"),
		episode(ep3, "Trois", "2026-03-14 15:30"),
		{ItemID: film, Kind: domain.ItemMovie, Title: "Film", StartedAt: at("2026-03-14 21:00"), Watched: 2 * time.Hour},
		// January 1 at 00:30 in Paris is still December 31 in UTC.
		{ItemID: track, Kind: domain.ItemTrack, ArtistID: artist, Title: "Piste", Subtitle: "Artiste", StartedAt: at("2026-01-01 00:30"), Watched: 4 * time.Minute},
		// An episode forgotten since: counted by its title.
		{Kind: domain.ItemEpisode, Title: "Pilote", Subtitle: "Autre série", StartedAt: at("2026-05-02 20:00"), Watched: 40 * time.Minute},
		// The year before: outside 2026.
		episode(ep1, "Un", "2025-12-31 23:00"),
	}
	st := Compute(plays, genres, 2026, paris)
	if st.Plays != 7 || st.Total != 4*24*time.Minute+2*time.Hour+4*time.Minute+40*time.Minute {
		t.Errorf("total: %d plays, %v", st.Plays, st.Total)
	}
	if st.Movies != 1 || st.Episodes != 4 || st.Series != 2 || st.Tracks != 1 {
		t.Errorf("counts: %d movies, %d episodes, %d series, %d tracks", st.Movies, st.Episodes, st.Series, st.Tracks)
	}
	if len(st.TopSeries) != 2 || st.TopSeries[0].Name != "Série" || st.TopSeries[0].Plays != 4 || st.TopSeries[1].ID != nil {
		t.Errorf("series: %+v", st.TopSeries)
	}
	// Drame: the series (96 min) and the movie (2 h). Anime: the series alone.
	if len(st.TopGenres) != 2 || st.TopGenres[0].Name != "Drame" || st.TopGenres[0].Time != 96*time.Minute+2*time.Hour {
		t.Errorf("genres: %+v", st.TopGenres)
	}
	if len(st.TopTracks) != 1 || st.TopTracks[0].Name != "Artiste — Piste" || len(st.TopArtists) != 1 || st.TopArtists[0].ID != artist {
		t.Errorf("music: %+v %+v", st.TopTracks, st.TopArtists)
	}
	if len(st.Timeline) != 12 || st.Timeline[2].Time != 4*24*time.Minute+2*time.Hour || st.Timeline[0].Time != 4*time.Minute {
		t.Errorf("months: %+v", st.Timeline)
	}
	// Saturday March 14, 9 pm Paris time: day 5 (Monday is 0).
	if got := st.ByHour[5*24+21]; got != 2*time.Hour {
		t.Errorf("Saturday 9 pm: %v", got)
	}
	if st.BusiestDay == nil || !st.BusiestDay.Start.Equal(at("2026-03-14 00:00")) || st.BusiestDay.Time != 4*24*time.Minute+2*time.Hour {
		t.Errorf("busiest day: %+v", st.BusiestDay)
	}
	if st.Binge == nil || st.Binge.Episodes != 3 || st.Binge.Series != "Série" || st.Binge.Time != 96*time.Minute {
		t.Errorf("binge: %+v", st.Binge)
	}

	// All time: one line per year.
	all := Compute(plays, genres, 0, paris)
	if all.Plays != 8 || len(all.Timeline) != 2 || all.Timeline[0].Start.Year() != 2025 || all.Timeline[1].Time != st.Total {
		t.Errorf("all time: %d plays, %+v", all.Plays, all.Timeline)
	}
	// In UTC the track of January 1 at 00:30 belongs to 2025.
	if utc := Compute(plays, genres, 2026, time.UTC); utc.Plays != 6 {
		t.Errorf("time zone: %d plays in 2026 (UTC)", utc.Plays)
	}
}

func TestCountsAsPlay(t *testing.T) {
	cases := []struct {
		kind              domain.ItemKind
		watched, duration time.Duration
		want              bool
	}{
		{domain.ItemMovie, 59 * time.Second, 2 * time.Hour, false},
		{domain.ItemMovie, time.Minute, 2 * time.Hour, true},
		{domain.ItemTrack, 30 * time.Second, 4 * time.Minute, true},
		{domain.ItemTrack, 20 * time.Second, 4 * time.Minute, false},
		{domain.ItemEpisode, 6 * time.Second, 12 * time.Second, true}, // half of a short item
		{domain.ItemEpisode, 0, 0, false},
	}
	for _, c := range cases {
		if got := domain.CountsAsPlay(c.kind, c.watched, c.duration); got != c.want {
			t.Errorf("%s %v / %v: %v", c.kind, c.watched, c.duration, got)
		}
	}
}

// A busy year: 20,000 plays (around fifty a day) over 300 movies, 1,000 episodes of 40 series and
// 2,000 tracks.
func BenchmarkCompute20000(b *testing.B) {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		b.Fatal(err)
	}
	pool := func(n int) []*domain.ID {
		out := make([]*domain.ID, n)
		for i := range out {
			out[i] = id()
		}
		return out
	}
	movies, shows, episodes, tracks, artists := pool(300), pool(40), pool(1000), pool(2000), pool(150)
	genres := map[domain.ID][]string{}
	for _, s := range append(shows, movies...) {
		genres[*s] = []string{"Drame", "Anime"}
	}
	start := time.Date(2026, 1, 1, 8, 0, 0, 0, loc)
	plays := make([]domain.Play, 20000)
	for i := range plays {
		p := domain.Play{StartedAt: start.Add(time.Duration(i) * 26 * time.Minute), Watched: 20 * time.Minute, Title: "x"}
		switch i % 3 {
		case 0:
			p.Kind, p.ItemID, p.SeriesID, p.Subtitle = domain.ItemEpisode, episodes[i%len(episodes)], shows[i%len(shows)], "Série"
		case 1:
			p.Kind, p.ItemID, p.ArtistID, p.Subtitle = domain.ItemTrack, tracks[i%len(tracks)], artists[i%len(artists)], "Artiste"
		default:
			p.Kind, p.ItemID = domain.ItemMovie, movies[i%len(movies)]
		}
		plays[i] = p
	}
	for b.Loop() {
		Compute(plays, genres, 2026, loc)
	}
}
