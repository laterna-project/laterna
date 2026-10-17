package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/media/subtitles"
	"github.com/laterna-project/laterna/internal/metadata"
	"github.com/laterna-project/laterna/internal/store"
)

// Lyrics of tracks: a .lrc file next to the track (written by LRCGET or a tagger), otherwise the
// lyrics in its tags. Where they are is noted at analysis time, and again when the folder of the
// album changes (a .lrc counts as metadata for the scan); the words are read when asked for.

// maxLyricsSize caps a .lrc file read.
const maxLyricsSize = 1 << 20

// lrcPath returns the .lrc file next to a track, with the same name, "" if there is none.
func lrcPath(track string) string {
	base := strings.TrimSuffix(track, filepath.Ext(track))
	for _, ext := range []string{".lrc", ".LRC"} {
		if fi, err := os.Stat(base + ext); err == nil && fi.Mode().IsRegular() {
			return base + ext
		}
	}
	return ""
}

// lyricsSource says where the lyrics of a track file are.
func lyricsSource(f domain.MediaFile) domain.LyricsSource {
	switch {
	case lrcPath(f.Path) != "":
		return domain.LyricsFile
	case metadata.LyricsTag(f.Info.Tags) != "":
		return domain.LyricsTags
	}
	return domain.LyricsNone
}

// refreshLyrics notes again where the lyrics of the tracks of an album are, when its folder
// changed.
func (a *App) refreshLyrics(ctx context.Context, lib domain.Library, albumID domain.ID) error {
	read := a.store.Read()
	files, err := read.AlbumFiles(ctx, albumID)
	if err != nil {
		return err
	}
	type change struct {
		track domain.ID
		src   domain.LyricsSource
	}
	var changes []change
	for _, ref := range files {
		f, err := read.File(ctx, ref.ID)
		if err != nil {
			return err
		}
		trackID, err := read.FileItem(ctx, f.ID)
		if store.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		t, err := read.Track(ctx, trackID)
		if store.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if src := lyricsSource(f); src != t.Lyrics {
			changes = append(changes, change{trackID, src})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	ids := make([]domain.ID, len(changes))
	err = a.store.Write(ctx, func(q store.Q) error {
		for i, c := range changes {
			ids[i] = c.track
			if err := q.SetTrackLyrics(ctx, c.track, c.src); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		a.itemsChanged(lib.ID, ids...)
	}
	return err
}

// Lyrics returns the lyrics of a track the caller can see: its .lrc file, otherwise its tags.
func (a *App) Lyrics(ctx context.Context, p domain.Principal, trackID domain.ID) (domain.Lyrics, error) {
	v, err := viewerOf(p)
	if err != nil {
		return domain.Lyrics{}, err
	}
	read := a.store.Read()
	view, err := read.View(ctx, v, trackID)
	if store.IsNotFound(err) || (err == nil && view.Item.Kind != domain.ItemTrack) {
		return domain.Lyrics{}, domain.NotFound("catalog.item_not_found")
	}
	if err != nil {
		return domain.Lyrics{}, err
	}
	files, err := read.ItemFiles(ctx, trackID)
	if err != nil {
		return domain.Lyrics{}, err
	}
	for _, fv := range files {
		f := fv.File
		if f.MissingSince != nil {
			continue
		}
		if p := lrcPath(f.Path); p != "" {
			text, err := readLyrics(p)
			if err != nil {
				return domain.Lyrics{}, err
			}
			lines, synced := metadata.ParseLyrics(text)
			return domain.Lyrics{Source: domain.LyricsFile, Synced: synced, Lines: lines}, nil
		}
		if text := metadata.LyricsTag(f.Info.Tags); text != "" {
			lines, synced := metadata.ParseLyrics(text)
			return domain.Lyrics{Source: domain.LyricsTags, Synced: synced, Lines: lines}, nil
		}
	}
	return domain.Lyrics{}, domain.NotFound("music.no_lyrics")
}

// readLyrics reads a .lrc file, in UTF-8 whatever it was written in (UTF-16, Windows-1252).
func readLyrics(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", domain.NotFound("music.no_lyrics")
		}
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxLyricsSize))
	if err != nil {
		return "", err
	}
	return string(subtitles.ToUTF8(b)), nil
}
