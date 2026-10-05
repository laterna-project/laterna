package store

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestSubtitleSets(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	lib := newLibrary("Shows", domain.LibraryShows, "/s")
	ep1 := domain.MediaFile{ID: domain.NewID(), LibraryID: lib.ID, Path: "/s/e1.mkv", Size: 1, ModTime: t0, Fingerprint: "e1"}
	ep2 := domain.MediaFile{ID: domain.NewID(), LibraryID: lib.ID, Path: "/s/e2.mkv", Size: 1, ModTime: t0, Fingerprint: "e2"}
	mustWrite(t, st, func(q Q) error {
		if err := q.CreateLibrary(ctx, lib); err != nil {
			return err
		}
		if err := q.CreateFile(ctx, ep1, t0); err != nil {
			return err
		}
		return q.CreateFile(ctx, ep2, t0)
	})
	if _, ok, err := st.Read().SubtitleSet(ctx, ep1.ID); ok || err != nil {
		t.Fatalf("never extracted: %v %v", ok, err)
	}

	shared := domain.Font{SHA256: "aa11", Names: []string{"go", "go regular"}, Ext: ".ttf", Size: 10}
	own := domain.Font{SHA256: "bb22", Names: []string{"title"}, Ext: ".otf", Size: 20}
	set := domain.SubtitleSet{
		Fingerprint: "e1", Sidecars: "sig", ExtractedAt: t0,
		Subtitles: []domain.Subtitle{
			{Position: 0, StreamIndex: 2, Codec: "ass", Language: "fre", Title: "Dialogues", Default: true, Formats: []string{"ass", "vtt"}},
			{Position: 1, StreamIndex: 3, Codec: "hdmv_pgs_subtitle", Language: "jpn", Forced: true, Formats: []string{"sup"}, Width: 1920, Height: 1080},
			{Position: 2, StreamIndex: -1, Path: "/s/e1.en.sdh.srt", Codec: "subrip", Language: "eng", HearingImpaired: true, Formats: []string{"vtt"}},
		},
		Fonts: []domain.Font{shared, own},
	}
	mustWrite(t, st, func(q Q) error {
		if err := q.SetSubtitleSet(ctx, ep1.ID, set); err != nil {
			return err
		}
		return q.SetSubtitleSet(ctx, ep2.ID, domain.SubtitleSet{Fingerprint: "e2", ExtractedAt: t0, Fonts: []domain.Font{shared}})
	})
	got, ok, err := st.Read().SubtitleSet(ctx, ep1.ID)
	if err != nil || !ok || !reflect.DeepEqual(got, set) {
		t.Fatalf("read back:\n%+v\nwant:\n%+v\n%v %v", got, set, ok, err)
	}
	if !got.Subtitles[1].Image() || got.Subtitles[0].Image() || !got.Subtitles[2].External() {
		t.Error("Image / External")
	}
	if f, err := st.Read().Font(ctx, "bb22"); err != nil || f.Ext != ".otf" || !slices.Equal(f.Names, []string{"title"}) {
		t.Errorf("font: %+v %v", f, err)
	}

	// Extracted again with no subtitle and no font: the font only e1 used belongs to nobody now,
	// the shared font stays (e2 uses it).
	mustWrite(t, st, func(q Q) error {
		if err := q.SetSubtitleSet(ctx, ep1.ID, domain.SubtitleSet{Fingerprint: "e1b", ExtractedAt: t0}); err != nil {
			return err
		}
		orphans, err := q.DeleteOrphanFonts(ctx)
		if err != nil {
			return err
		}
		if len(orphans) != 1 || orphans[0].SHA256 != "bb22" || orphans[0].Ext != ".otf" {
			t.Errorf("orphan fonts: %+v", orphans)
		}
		return nil
	})
	if got, ok, _ := st.Read().SubtitleSet(ctx, ep1.ID); !ok || len(got.Subtitles) != 0 || len(got.Fonts) != 0 || got.Fingerprint != "e1b" {
		t.Errorf("after extracting again: %+v", got)
	}
	// The scan sees the extraction state along with each file.
	known, err := st.Read().LibraryFiles(ctx, lib.ID)
	if err != nil || len(known) != 2 {
		t.Fatal(known, err)
	}
	for _, k := range known {
		if k.ID == ep1.ID && (k.SubtitlesFingerprint != "e1b" || k.SubtitlesSidecars != "") {
			t.Errorf("known file: %+v", k)
		}
	}
	// File forgotten: its subtitles too, and the shared font becomes an orphan.
	mustWrite(t, st, func(q Q) error {
		if err := q.DeleteFile(ctx, ep2.ID); err != nil {
			return err
		}
		orphans, err := q.DeleteOrphanFonts(ctx)
		if len(orphans) != 1 || orphans[0].SHA256 != "aa11" {
			t.Errorf("after forgetting: %+v", orphans)
		}
		return err
	})
	if ids, err := st.Read().SubtitleSetFiles(ctx); err != nil || !slices.Equal(ids, []domain.ID{ep1.ID}) {
		t.Errorf("extracted files: %v %v", ids, err)
	}
}
