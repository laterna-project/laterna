package store

import (
	"context"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestNextUp(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	next := func() []string {
		t.Helper()
		views, err := f.st.Read().NextUp(ctx, f.viewer(), 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range views {
			out = append(out, v.Item.GroupKey)
		}
		return out
	}
	if got := next(); len(got) != 0 {
		t.Errorf("nothing played, nothing next: %v", got)
	}
	played := func(i int, at time.Time) {
		t.Helper()
		mustWrite(t, f.st, func(q Q) error { return q.SetPlayed(ctx, f.profile, []domain.ID{f.eps[i].ID}, true, at) })
	}
	played(0, t0) // S1E1
	if got := next(); len(got) != 1 || got[0] != "episode:1:2" {
		t.Errorf("after S1E1: %v", got)
	}
	played(1, t0.Add(time.Hour)) // S1E2: on to the next season
	if got := next(); len(got) != 1 || got[0] != "episode:2:1" {
		t.Errorf("after S1E2: %v", got)
	}
	// Rewatching S1E1 later: next up starts again from there, and since S1E2 is already played it
	// jumps to S2E1.
	played(0, t0.Add(2*time.Hour))
	if got := next(); len(got) != 1 || got[0] != "episode:2:1" {
		t.Errorf("after rewatching S1E1: %v", got)
	}
	played(2, t0.Add(3*time.Hour)) // S2E1: series finished
	if got := next(); len(got) != 0 {
		t.Errorf("series finished: %v", got)
	}
}

func TestResumeAndProgress(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	read := f.st.Read()
	amelie, brazil := f.movies["Amélie"], f.movies["Brazil"]
	mustWrite(t, f.st, func(q Q) error {
		if err := q.SaveProgress(ctx, f.profile, amelie.ID, 20*time.Minute, false, t0); err != nil {
			return err
		}
		return q.SaveProgress(ctx, f.profile, brazil.ID, 50*time.Minute, false, t0.Add(time.Hour))
	})
	views, err := read.Resume(ctx, f.viewer(), 10)
	if err != nil || len(views) != 2 || views[0].Item.ID != brazil.ID || views[1].UserData.Position != 20*time.Minute {
		t.Fatalf("to resume (most recent first): %+v %v", views, err)
	}

	// Finished: played, one more play, nothing left to resume.
	mustWrite(t, f.st, func(q Q) error { return q.SaveProgress(ctx, f.profile, brazil.ID, 0, true, t0.Add(2*time.Hour)) })
	v, err := read.View(ctx, f.viewer(), brazil.ID)
	if err != nil || !v.UserData.Played || v.UserData.PlayCount != 1 || v.UserData.Position != 0 {
		t.Errorf("finished playback: %+v %v", v.UserData, err)
	}
	if views, _ := read.Resume(ctx, f.viewer(), 10); len(views) != 1 || views[0].Item.ID != amelie.ID {
		t.Errorf("to resume once Brazil is finished: %+v", views)
	}
	// A vanished file: nothing to resume there anymore.
	mustWrite(t, f.st, func(q Q) error { return q.MarkFileMissing(ctx, f.files[amelie.ID], t0) })
	if views, _ := read.Resume(ctx, f.viewer(), 10); len(views) != 0 {
		t.Errorf("vanished movie still to resume: %+v", views)
	}
}

func TestLatestSeries(t *testing.T) {
	f := newCatalogFixture(t)
	views, err := f.st.Read().LatestSeries(context.Background(), f.viewer(), f.lib.ID, 10)
	if err != nil || len(views) != 1 || views[0].Item.ID != f.series.ID || views[0].EpisodeCount != 3 {
		t.Errorf("latest series: %+v %v", views, err)
	}
	// One episode per batch: the cursor moves from batch to batch and the series is returned only
	// once.
	saved := latestBatch
	latestBatch = 1
	t.Cleanup(func() { latestBatch = saved })
	views, err = f.st.Read().LatestSeries(context.Background(), f.viewer(), f.lib.ID, 10)
	if err != nil || len(views) != 1 || views[0].Item.ID != f.series.ID {
		t.Errorf("latest series, one episode per batch: %+v %v", views, err)
	}
}

// One read connection is enough for the home page: no read waits for a second one while holding the
// first. Otherwise, under load, each call would hold a pool connection while waiting for another
// and the server would lock up.
func TestHomeNeedsOneConnection(t *testing.T) {
	f := newCatalogFixture(t)
	f.st.reader.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	q := f.st.Read()
	if _, err := q.Resume(ctx, f.viewer(), 10); err != nil {
		t.Error("resume:", err)
	}
	if _, err := q.NextUp(ctx, f.viewer(), 10); err != nil {
		t.Error("next up:", err)
	}
	// Limit 1: the series is picked before all episodes have been read.
	if _, err := q.LatestSeries(ctx, f.viewer(), f.lib.ID, 1); err != nil {
		t.Error("latest series:", err)
	}
	if _, err := q.RecentAlbums(ctx, f.viewer(), 10); err != nil {
		t.Error("recent albums:", err)
	}
}
