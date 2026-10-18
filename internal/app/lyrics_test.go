package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// Lyrics come from a .lrc file next to a track, then from its tags. Where they are follows the
// .lrc as it comes and goes; the words are read when asked for.
func TestLyrics(t *testing.T) {
	a, _ := startMediaApp(t)
	_, p := setupAdmin(t, a)
	ctx := context.Background()
	ffmpeg, _, _ := testfixtures.FFmpeg()
	album := filepath.Join(t.TempDir(), "Music", "Artist Test", "Album Test (2020)")
	mustNil(t, os.MkdirAll(album, 0o750))
	// Track One with lyrics in its tags, Track Two with a .lrc next to it (Windows-1252, as old
	// tools write it).
	if out, err := exec.Command(ffmpeg, "-v", "error", "-i", "file:"+testRoot("Music/Artist Test/Album Test (2020)/01 - Track One.flac"),
		"-c", "copy", "-metadata", "LYRICS=First verse\nstill the first\n\nSecond verse",
		"file:"+filepath.Join(album, "01 - Track One.flac")).CombinedOutput(); err != nil {
		t.Fatalf("tagged copy: %v %s", err, out)
	}
	copyFixture(t, "Music/Artist Test/Album Test (2020)/02 - Track Two.mp3", filepath.Join(album, "02 - Track Two.mp3"))
	lrc := filepath.Join(album, "02 - Track Two.lrc")
	mustNil(t, os.WriteFile(lrc, []byte("[ti:Track Two]\n[00:01.50]Caf\xe9 au lait\n[00:00.50]Intro\n"), 0o600))
	lib, err := a.CreateLibrary(ctx, "Music", domain.LibraryMusic, []string{filepath.Dir(filepath.Dir(album))})
	mustNil(t, err)
	waitIdle(t, a)

	tracksOf := func() (one, two domain.ItemView) {
		t.Helper()
		albums, err := a.ListAlbums(ctx, p, ListQuery{})
		mustNil(t, err)
		if len(albums.Items) != 1 {
			t.Fatalf("albums: %+v", albums.Items)
		}
		_, _, tracks, err := a.Album(ctx, p, albums.Items[0].Item.ID)
		mustNil(t, err)
		if len(tracks) != 2 || tracks[0].Track == nil || tracks[1].Track == nil {
			t.Fatalf("tracks: %+v", tracks)
		}
		return tracks[0], tracks[1]
	}
	one, two := tracksOf()
	if one.Track.Lyrics != domain.LyricsTags || two.Track.Lyrics != domain.LyricsFile {
		t.Errorf("lyrics: %q and %q", one.Track.Lyrics, two.Track.Lyrics)
	}
	plain, err := a.Lyrics(ctx, p, one.Item.ID)
	mustNil(t, err)
	if plain.Synced || plain.Source != domain.LyricsTags || len(plain.Lines) != 4 || plain.Lines[3].Text != "Second verse" {
		t.Errorf("lyrics from the tags: %+v", plain)
	}
	synced, err := a.Lyrics(ctx, p, two.Item.ID)
	mustNil(t, err)
	if !synced.Synced || synced.Source != domain.LyricsFile || len(synced.Lines) != 2 ||
		synced.Lines[0] != (domain.LyricLine{Start: 500 * time.Millisecond, Text: "Intro"}) || synced.Lines[1].Text != "Café au lait" {
		t.Errorf("lyrics from the .lrc: %+v", synced)
	}

	// The .lrc goes away: Track Two has no lyrics any more. It comes back: so do they.
	mustNil(t, os.Remove(lrc))
	mustNil(t, a.ScanLibrary(ctx, lib.ID))
	waitIdle(t, a)
	if _, two := tracksOf(); two.Track.Lyrics != domain.LyricsNone {
		t.Errorf("after the .lrc went away: %q", two.Track.Lyrics)
	}
	if _, err := a.Lyrics(ctx, p, two.Item.ID); domain.CodeOf(err) != "music.no_lyrics" {
		t.Errorf("no lyrics: %v", err)
	}
	mustNil(t, os.WriteFile(lrc, []byte("[00:02.00]Back\n"), 0o600))
	mustNil(t, a.ScanLibrary(ctx, lib.ID))
	waitIdle(t, a)
	if _, two := tracksOf(); two.Track.Lyrics != domain.LyricsFile {
		t.Errorf("after the .lrc came back: %q", two.Track.Lyrics)
	}
	if _, err := a.Lyrics(ctx, p, one.Track.AlbumID); !isKind(err, domain.ErrNotFound) {
		t.Errorf("lyrics of an album: %v", err)
	}
}
