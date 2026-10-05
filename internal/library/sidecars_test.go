package library

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/naming"
)

func touch(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func rels(subs []Sidecar) []string {
	var out []string
	for _, s := range subs {
		out = append(out, s.Rel)
	}
	return out
}

// The scan (Walk, the whole library) and the extraction job (Near, the video's folder) must find
// the same external subtitles, with the same signature.
func TestSidecars(t *testing.T) {
	root := t.TempDir()
	touch(t, root,
		"Movie (2020)/Movie (2020).mkv", "Movie (2020)/Movie (2020).fr.srt", "Movie (2020)/Movie (2020).en.forced.ass",
		"Movie (2020)/Subs/2_English.srt", "Movie (2020)/Movie (2020).idx", "Movie (2020)/Movie (2020).sub",
		"Movie (2020)/Other.srt",
		"Show/S01/Show.S01E01.mkv", "Show/S01/Show.S01E02.mkv", "Show/S01/Show.S01E01.ja.ass",
		"Show/S01/Subs/Show.S01E02/3_French.srt", "Show/S01/Subs/1_English.srt",
	)
	w, err := Walk(context.Background(), []string{root}, naming.IsVideo)
	if err != nil || len(w.Entries) != 3 || len(w.Subtitles) != 9 {
		t.Fatalf("walk: %d videos, %d subtitles, %v", len(w.Entries), len(w.Subtitles), err)
	}
	all := Sidecars(w)
	movie := filepath.Join(root, "Movie (2020)", "Movie (2020).mkv")
	want := []string{"Movie (2020)/Movie (2020).en.forced.ass", "Movie (2020)/Movie (2020).fr.srt", "Movie (2020)/Movie (2020).idx", "Movie (2020)/Subs/2_English.srt"}
	subs := all[movie]
	if got := rels(subs); !slices.Equal(got, want) || len(subs) == 0 {
		t.Fatalf("movie: %v", got)
	}
	if s := subs[0]; s.Language != "eng" || !s.Forced {
		t.Errorf("description: %+v", s.Sidecar)
	}
	e1, e2 := filepath.Join(root, "Show", "S01", "Show.S01E01.mkv"), filepath.Join(root, "Show", "S01", "Show.S01E02.mkv")
	if got := rels(all[e1]); !slices.Equal(got, []string{"Show/S01/Show.S01E01.ja.ass"}) {
		t.Errorf("episode 1: %v", got)
	}
	if got := rels(all[e2]); !slices.Equal(got, []string{"Show/S01/Subs/Show.S01E02/3_French.srt"}) {
		t.Errorf("episode 2 (the shared \"Subs\" belongs to neither): %v", got)
	}

	video := func(w Walked, path string) Entry {
		i := slices.IndexFunc(w.Entries, func(e Entry) bool { return e.Path == path })
		if i < 0 {
			t.Fatalf("%s missing", path)
		}
		return w.Entries[i]
	}
	for _, path := range []string{movie, e1, e2} {
		near, err := Near(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		fromScan := Signature(video(w, path), all[path])
		fromJob := Signature(video(near, path), Sidecars(near)[path])
		if fromScan == "" || fromScan != fromJob {
			t.Errorf("%s: signature %q from the scan, %q from the job", filepath.Base(path), fromScan, fromJob)
		}
	}

	// A changed subtitle changes the signature. No subtitle, no signature.
	before := Signature(video(w, movie), all[movie])
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "Movie (2020)", "Movie (2020).fr.srt"), future, future); err != nil {
		t.Fatal(err)
	}
	w2, _ := Walk(context.Background(), []string{root}, naming.IsVideo)
	if Signature(video(w2, movie), Sidecars(w2)[movie]) == before {
		t.Error("signature unchanged after an edit")
	}
	if Signature(Entry{}, nil) != "" {
		t.Error("signature without any subtitle")
	}
}
