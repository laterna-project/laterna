package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func mkdirs(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o750); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mkdirs(t, root,
		"Shows/Season 10/", "Shows/Season 2/E01.mkv", "Shows/Season 2/E02.mkv", "Shows/Season 2/notes.txt",
		"Shows/Music/01 - Title.flac", "Shows/Music/cover.jpg", "Shows/Music/booklet.jpg",
		"Shows/Photos/IMG_2041.jpg", "Shows/Photos/poster.jpg", "Shows/poster.jpg", "Shows/Season 2/E01-thumb.jpg",
		"Shows/Empty/", "Shows/.cache/", "Shows/@eaDir/",
		"Shows/Laterna data/", "Shows/Movie.mkv", "Shows/Movie-trailer.mkv", "Shows/Books/Volume 1.cbz",
		"Shows/Books/Volume 2/",
	)
	dir := filepath.Join(root, "Shows")
	l, err := List(ctx, dir, []string{filepath.Join(dir, "Laterna data")})
	if err != nil {
		t.Fatal(err)
	}
	if l.Path != dir || l.Parent != root || l.Truncated {
		t.Errorf("folder: %q, parent %q, truncated %v", l.Path, l.Parent, l.Truncated)
	}
	if l.Media != (MediaCounts{Videos: 1}) {
		t.Errorf("media in the folder: %+v", l.Media)
	}
	var names []string
	byName := map[string]Folder{}
	for _, f := range l.Folders {
		names = append(names, f.Name)
		byName[f.Name] = f
	}
	if !slices.Equal(names, []string{"Books", "Empty", "Music", "Photos", "Season 2", "Season 10"}) {
		t.Fatalf("subfolders: %q", names)
	}
	for name, want := range map[string]Folder{
		"Books":     {Readable: true, HasSubfolders: true, Media: MediaCounts{Books: 1}},
		"Music":     {Readable: true, Media: MediaCounts{Audio: 1}},
		"Photos":    {Readable: true, Media: MediaCounts{Photos: 1}},
		"Season 2":  {Readable: true, Media: MediaCounts{Videos: 2}},
		"Season 10": {Readable: true},
		"Empty":     {Readable: true},
	} {
		got := byName[name]
		if got.Path != filepath.Join(dir, name) || got.Readable != want.Readable || got.HasSubfolders != want.HasSubfolders || got.Media != want.Media {
			t.Errorf("%s: %+v", name, got)
		}
	}

	if _, err := List(ctx, "relative", nil); err == nil {
		t.Error("relative path accepted")
	}
	if _, err := List(ctx, filepath.Join(root, "absent"), nil); !os.IsNotExist(err) {
		t.Errorf("missing folder: %v", err)
	}
	if _, err := List(ctx, filepath.Join(dir, "Movie.mkv"), nil); err == nil {
		t.Error("file accepted as a folder")
	}
}

func TestListTruncated(t *testing.T) {
	root := t.TempDir()
	for i := range maxFolders + 5 {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d%04d", i)), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	l, err := List(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Truncated || len(l.Folders) != maxFolders || l.Folders[0].Name != "d0000" {
		t.Errorf("truncated %v, %d folders, first %q", l.Truncated, len(l.Folders), l.Folders[0].Name)
	}
}

func TestStarts(t *testing.T) {
	starts := Starts(context.Background())
	want := "/"
	if runtime.GOOS == "windows" {
		want = filepath.VolumeName(t.TempDir()) + `\`
	}
	if !slices.ContainsFunc(starts, func(f Folder) bool { return strings.EqualFold(f.Path, want) && f.Readable }) {
		t.Errorf("starting points without %q: %+v", want, starts)
	}
}
