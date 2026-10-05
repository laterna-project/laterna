package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

// Segments: the episodes of "Café Stories" share a 3 s intro with no chapter, which only the audio
// gives away; those of "Anime Test" have "Opening" and "Ending" chapters. An episode alone in its
// season has nothing to compare with.
func TestSegmentsEndToEnd(t *testing.T) {
	a, _ := startMediaApp(t, func(o *Options) { o.SegmentMin = 2 * time.Second })
	ctx := context.Background()
	lib, err := a.CreateLibrary(ctx, "Shows", domain.LibraryShows, []string{testRoot("Shows"), testRoot("Anime")}, "")
	mustNil(t, err)
	waitIdle(t, a)
	fileOf := func(it domain.Item) domain.MediaFile {
		t.Helper()
		files, err := a.store.Read().ItemFiles(ctx, it.ID)
		if err != nil || len(files) != 1 {
			t.Fatalf("files of %s: %v %v", it.Title, files, err)
		}
		return files[0].File
	}
	segment := func(f domain.MediaFile, kind domain.SegmentKind) *domain.Segment {
		for i := range f.Segments {
			if f.Segments[i].Kind == kind {
				return &f.Segments[i]
			}
		}
		return nil
	}

	show := itemByKey(t, a, lib, "series:Café Stories (2022)")
	for ep := 1; ep <= 3; ep++ {
		f := fileOf(itemByKey(t, a, lib, fmt.Sprintf("episode:%s:1:%d", show.ID, ep)))
		intro := segment(f, domain.SegmentIntro)
		if intro == nil || intro.Source != domain.SegmentFromAudio || intro.Start > 300*time.Millisecond ||
			intro.End < 2600*time.Millisecond || intro.End > 3300*time.Millisecond {
			t.Errorf("S01E%02d: intro %+v", ep, intro)
		}
		if c := segment(f, domain.SegmentCredits); c != nil {
			t.Errorf("S01E%02d: made-up credits %+v", ep, c)
		}
		prints, _ := os.ReadDir(filepath.Join(a.cacheDir, "fingerprints", f.ID.String()[:2], f.ID.String()))
		if len(prints) != 2 {
			t.Errorf("S01E%02d: cached fingerprints %v", ep, prints)
		}
	}
	if f := fileOf(itemByKey(t, a, lib, fmt.Sprintf("episode:%s:2:1", show.ID))); len(f.Segments) != 0 {
		t.Errorf("S02E01, alone in its season: %+v", f.Segments)
	}

	anime := itemByKey(t, a, lib, "series:Anime Test")
	for ep := 1; ep <= 3; ep++ {
		f := fileOf(itemByKey(t, a, lib, fmt.Sprintf("episode:%s:1:%d", anime.ID, ep)))
		want := []domain.Segment{
			{Kind: domain.SegmentIntro, Start: 0, End: 3 * time.Second, Source: domain.SegmentFromChapters},
			{Kind: domain.SegmentCredits, Start: 9 * time.Second, End: 12 * time.Second, Source: domain.SegmentFromChapters},
		}
		if len(f.Segments) != 2 || f.Segments[0] != want[0] || f.Segments[1] != want[1] {
			t.Errorf("anime %d: %+v", ep, f.Segments)
		}
	}
}
