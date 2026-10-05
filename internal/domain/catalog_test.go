package domain

import (
	"testing"
	"time"
)

func TestProgress(t *testing.T) {
	const film = 100 * time.Minute
	tests := []struct {
		position, runtime time.Duration
		resume            time.Duration
		finished          bool
	}{
		{30 * time.Second, film, 0, false},              // under a minute
		{4 * time.Minute, film, 0, false},               // under 5%
		{5 * time.Minute, film, 5 * time.Minute, false}, // resume
		{89 * time.Minute, film, 89 * time.Minute, false},
		{90 * time.Minute, film, 0, true}, // end credits
		{120 * time.Minute, film, 0, true},
		{40 * time.Minute, 0, 40 * time.Minute, false}, // unknown runtime: never finished
		{30 * time.Second, 0, 0, false},
	}
	for _, tt := range tests {
		resume, finished := Progress(tt.position, tt.runtime)
		if resume != tt.resume || finished != tt.finished {
			t.Errorf("Progress(%v, %v) = %v, %v; want %v, %v", tt.position, tt.runtime, resume, finished, tt.resume, tt.finished)
		}
	}
}

func TestItemSortValid(t *testing.T) {
	for _, s := range []ItemSort{SortTitle, SortAdded, SortReleased, SortRating} {
		if !s.Valid() {
			t.Errorf("%s rejected", s)
		}
	}
	if ItemSort("bogus").Valid() || ItemSort("").Valid() {
		t.Error("unknown order accepted")
	}
}
