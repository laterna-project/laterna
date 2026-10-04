package naming

import (
	"slices"
	"testing"
)

func TestNaturalCompare(t *testing.T) {
	got := []string{"Saison 10", "saison 2", "Saison 1", "Bonus", "page007.jpg", "page10.jpg", "page8.jpg", "Saison 02b"}
	slices.SortStableFunc(got, NaturalCompare)
	want := []string{"Bonus", "page007.jpg", "page8.jpg", "page10.jpg", "Saison 1", "saison 2", "Saison 02b", "Saison 10"}
	if !slices.Equal(got, want) {
		t.Errorf("order: %q", got)
	}
}
