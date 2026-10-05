package naming

import (
	"slices"
	"testing"
)

func TestNaturalCompare(t *testing.T) {
	got := []string{"Season 10", "season 2", "Season 1", "Bonus", "page007.jpg", "page10.jpg", "page8.jpg", "Season 02b"}
	slices.SortStableFunc(got, NaturalCompare)
	want := []string{"Bonus", "page007.jpg", "page8.jpg", "page10.jpg", "Season 1", "season 2", "Season 02b", "Season 10"}
	if !slices.Equal(got, want) {
		t.Errorf("order: %q", got)
	}
}
