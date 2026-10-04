package store

import (
	"context"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Taste signals: the time from the history, and at least the position reached in a movie that was
// started.
func TestTasteSignals(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	prof, movie := historyFixture(t, st)
	mustWrite(t, st, func(q Q) error {
		if err := q.AddPlay(ctx, play(prof.ID, movie.ID, t0)); err != nil { // one hour watched
			return err
		}
		return q.SaveProgress(ctx, prof.ID, movie.ID, 90*time.Minute, false, t0.Add(2*time.Hour))
	})
	signals, err := st.Read().TasteSignals(ctx, prof.ID)
	if err != nil || len(signals) != 1 {
		t.Fatalf("signals: %+v %v", signals, err)
	}
	s := signals[0]
	if s.ID != movie.ID || s.Watched != 90*time.Minute || !s.Started || s.Played || !s.LastAt.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("signal: %+v", s)
	}
	if other, err := st.Read().TasteSignals(ctx, domain.NewID()); err != nil || len(other) != 0 {
		t.Errorf("other profile: %+v %v", other, err)
	}
}
