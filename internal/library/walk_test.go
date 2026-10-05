package library

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/naming"
)

func write(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWalk(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Movies")
	for _, rel := range []string{
		"Movie (2020)/Movie (2020).mkv",
		"Movie (2020)/Extras/Making of.mkv",
		"Movie (2020)/movie-sample.mkv",
		"Movie (2020)/poster.jpg",
		"Movie (2020)/Movie (2020).srt",
		".cache/x.mkv",
		"Other.MP4",
	} {
		write(t, filepath.Join(root, filepath.FromSlash(rel)), []byte("x"))
	}
	w, err := Walk(context.Background(), []string{root, filepath.Join(t.TempDir(), "absent")}, naming.IsVideo)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, e := range w.Entries {
		rels = append(rels, e.Rel)
		if e.Root != root || e.Size != 1 || e.ModTime.IsZero() {
			t.Errorf("incomplete entry: %+v", e)
		}
	}
	slices.Sort(rels)
	if !slices.Equal(rels, []string{"Movie (2020)/Movie (2020).mkv", "Other.MP4"}) {
		t.Errorf("files kept: %v", rels)
	}
	if len(w.Unavailable) != 1 || w.Counts[root] != 2 {
		t.Errorf("roots: unavailable=%v counts=%v", w.Unavailable, w.Counts)
	}
	if len(w.Metadata) != 1 || w.Metadata[0].Rel != "Movie (2020)/poster.jpg" {
		t.Errorf("metadata: %+v", w.Metadata)
	}
}

func TestMetadataSignatures(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"Show/tvshow.nfo", "Show/poster.jpg", "Show/Season 1/E01.nfo", "Show/Season 1/E01.mkv"} {
		write(t, filepath.Join(root, filepath.FromSlash(rel)), []byte("x"))
	}
	walk := func() map[string]string {
		t.Helper()
		w, err := Walk(context.Background(), []string{root}, naming.IsVideo)
		if err != nil {
			t.Fatal(err)
		}
		return MetadataSignatures(w)
	}
	show, season := filepath.Join(root, "Show"), filepath.Join(root, "Show", "Season 1")
	before := walk()
	if len(before) != 2 || before[show] == "" || before[season] == "" {
		t.Fatalf("signatures: %v", before)
	}
	// An added thumbnail only changes the signature of its own folder.
	write(t, filepath.Join(season, "E01-thumb.jpg"), []byte("x"))
	after := walk()
	if after[show] != before[show] || after[season] == before[season] {
		t.Errorf("after adding: %v, before: %v", after, before)
	}
	// Rewritten NFO (different size): the signature changes.
	write(t, filepath.Join(show, "tvshow.nfo"), []byte("xy"))
	if walk()[show] == after[show] {
		t.Error("rewritten NFO: signature unchanged")
	}
}

func TestWalkCancelled(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.mkv"), []byte("x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Walk(ctx, []string{root}, naming.IsVideo); err == nil {
		t.Error("canceled walk: want an error")
	}
}

func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 20_000) // 320 KiB
	write(t, filepath.Join(dir, "a.mkv"), big)
	write(t, filepath.Join(dir, "sub", "renamed.mkv"), big)
	middle := slices.Clone(big)
	middle[len(middle)/2] ^= 0xff // changed in the middle, outside what is read
	write(t, filepath.Join(dir, "middle.mkv"), middle)
	end := slices.Clone(big)
	end[len(end)-1] ^= 0xff
	write(t, filepath.Join(dir, "end.mkv"), end)
	write(t, filepath.Join(dir, "small.mkv"), []byte("small"))

	fp := func(name string) string {
		t.Helper()
		s, err := Fingerprint(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := fp("a.mkv")
	if len(a) != 64 || a != fp("sub/renamed.mkv") {
		t.Error("a renamed file must keep its fingerprint")
	}
	if a != fp("middle.mkv") {
		t.Error("the fingerprint should only read the start and the end (accepted trade-off)")
	}
	if a == fp("end.mkv") || fp("small.mkv") == a {
		t.Error("different contents, same fingerprint")
	}
	if _, err := Fingerprint(filepath.Join(dir, "absent.mkv")); err == nil {
		t.Error("missing file: want an error")
	}
}

func TestUnder(t *testing.T) {
	dirs := []string{filepath.Join("m", "movies"), filepath.Join("m", "shows") + string(filepath.Separator)}
	for p, want := range map[string]bool{
		filepath.Join("m", "movies", "a.mkv"):     true,
		filepath.Join("m", "movies"):              true,
		filepath.Join("m", "movies2", "a.mkv"):    false,
		filepath.Join("m", "shows", "x", "y.mkv"): true,
	} {
		if got := Under(p, dirs); got != want {
			t.Errorf("Under(%q) = %v", p, got)
		}
	}
}
