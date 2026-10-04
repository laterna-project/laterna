package app

import (
	"context"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// History: one play per playback that was watched enough, time watched without seeks, offline plays
// counted once, statistics, deletion.
func TestHistoryAndStats(t *testing.T) {
	a, c, p, movies := moviesByTitle(t)
	ctx := context.Background()
	movie := movies["Big Test Movie"] // a twelve-second file
	start := func() PlayInfo {
		t.Helper()
		info, err := a.StartPlayback(ctx, p, PlayRequest{ItemID: movie.ID, Audio: -1, Device: browser})
		mustNil(t, err)
		return info
	}
	report := func(info PlayInfo, after, at time.Duration) {
		t.Helper()
		c.advance(after)
		mustNil(t, a.ReportProgress(p, info.SessionID, at))
	}

	// Played from 0 to 5 s, then to 11 s; going back does not count: 11 s watched out of 12.
	info := start()
	report(info, 5*time.Second, 5*time.Second)
	report(info, 6*time.Second, 11*time.Second)
	report(info, time.Second, 2*time.Second)
	report(info, time.Second, 3*time.Second)
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))
	// A seek (9 s in 1 s) does not count: 1 s watched, under the threshold (half of the 12 s).
	info = start()
	report(info, time.Second, time.Second)
	report(info, time.Second, 10*time.Second)
	mustNil(t, a.StopPlayback(ctx, p, info.SessionID, 0))

	page, err := a.History(ctx, p, "", 0)
	mustNil(t, err)
	if len(page.Entries) != 1 {
		t.Fatalf("history: %+v", page.Entries)
	}
	e := page.Entries[0]
	if e.Play.Watched != 12*time.Second || e.Play.Kind != domain.ItemMovie || e.Play.Title != "Big Test Movie" ||
		e.Play.Device != "PC" || e.View == nil || e.View.Item.ID != movie.ID {
		t.Errorf("play: %+v (card %v)", e.Play, e.View != nil)
	}

	// Offline: counted once, even if the same report is replayed.
	other := movies["Deux Pistes"]
	offline := []OfflinePlay{{ItemID: other.ID, Position: 12 * time.Second, Finished: true, At: a.now().Add(-time.Hour)}}
	mustNil(t, a.SyncOfflinePlayback(ctx, p, offline))
	mustNil(t, a.SyncOfflinePlayback(ctx, p, offline))
	first, err := a.History(ctx, p, "", 1)
	mustNil(t, err)
	if len(first.Entries) != 1 || first.NextPageToken == "" || first.Entries[0].Play.Title != "Big Test Movie" {
		t.Fatalf("first page: %+v", first)
	}
	second, err := a.History(ctx, p, first.NextPageToken, 1)
	mustNil(t, err)
	if len(second.Entries) != 1 || second.NextPageToken != "" || !second.Entries[0].Play.Offline || !second.Entries[0].Play.Completed {
		t.Fatalf("second page: %+v", second)
	}

	st, err := a.Stats(ctx, p, a.now().Year(), "Europe/Paris")
	mustNil(t, err)
	// Offline and finished, "Deux Pistes" counts its whole runtime (one minute according to its
	// NFO).
	if st.Plays != 2 || st.Movies != 2 || st.Total != 72*time.Second || len(st.TopMovies) != 2 || st.TopMovies[0].Name != "Deux Pistes" ||
		len(st.TopMovies[1].Images) == 0 || len(st.TopGenres) == 0 || len(st.Timeline) != 12 {
		t.Errorf("statistics: %+v", st)
	}
	if _, err := a.Stats(ctx, p, 0, "Not/A_Zone"); !isKind(err, domain.ErrInvalid) {
		t.Errorf("unknown time zone: %v", err)
	}

	mustNil(t, a.DeleteHistoryEntry(ctx, p, e.Play.ID))
	if err := a.DeleteHistoryEntry(ctx, p, e.Play.ID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("play already deleted: %v", err)
	}
	n, err := a.ClearHistory(ctx, p)
	if err != nil || n != 1 {
		t.Errorf("clear: %d %v", n, err)
	}
}

// Time watched: plausible progress between two reports. On the first one, a playback that started
// from the beginning rather than from the suggested resume point counts too.
func TestSessionAdvance(t *testing.T) {
	t0 := time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		resume  time.Duration
		reports [][2]time.Duration // [elapsed time, reported position]
		want    time.Duration
	}{
		{"from the resume point", 200 * time.Second, [][2]time.Duration{{10 * time.Second, 210 * time.Second}, {10 * time.Second, 220 * time.Second}}, 20 * time.Second},
		{"from the beginning despite the resume point", 200 * time.Second, [][2]time.Duration{{10 * time.Second, 9 * time.Second}, {10 * time.Second, 19 * time.Second}}, 19 * time.Second},
		{"seek forward", 0, [][2]time.Duration{{10 * time.Second, 10 * time.Second}, {time.Second, 300 * time.Second}, {10 * time.Second, 310 * time.Second}}, 20 * time.Second},
		{"seek backward", 0, [][2]time.Duration{{30 * time.Second, 30 * time.Second}, {time.Second, 5 * time.Second}, {10 * time.Second, 15 * time.Second}}, 40 * time.Second},
		{"pause", 0, [][2]time.Duration{{10 * time.Second, 10 * time.Second}, {10 * time.Minute, 10 * time.Second}}, 10 * time.Second},
	}
	for _, c := range cases {
		s := &playSession{lastPos: c.resume, lastAt: t0}
		now := t0
		for _, r := range c.reports {
			now = now.Add(r[0])
			s.advance(r[1], now)
		}
		if s.watched != c.want {
			t.Errorf("%s: %v watched, want %v", c.name, s.watched, c.want)
		}
	}
}
