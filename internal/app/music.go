package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/metadata"
	"github.com/laterna-project/laterna/internal/naming"
	"github.com/laterna-project/laterna/internal/proc"
	"github.com/laterna-project/laterna/internal/store"
)

// Music: artists, albums and tracks, taken from the tags of the audio files. The path is only a
// fallback.

// mediaOf returns the files a library indexes: audio for music, books and photos for their
// libraries, video otherwise.
func mediaOf(kind domain.LibraryKind) func(rel string) bool {
	switch kind {
	case domain.LibraryMusic:
		return naming.IsAudio
	case domain.LibraryBooks:
		return naming.IsBook
	case domain.LibraryPhotos:
		return naming.IsPhoto
	case domain.LibraryMovies, domain.LibraryShows:
	}
	return naming.IsVideo
}

// musicKey is the group key of a name: its normalized form, or the name itself in lower case if it
// has no letter or digit ("!!!").
func musicKey(name string) string {
	if k := naming.Key(name); k != "" {
		return k
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// placeTrack files an audio file: album artist, album, track. Tags win over the path. It returns
// the items whose metadata must be read again (the album, which sums up its tracks, and the artist
// if new).
func (a *App) placeTrack(ctx context.Context, q store.Q, lib domain.Library, f domain.MediaFile, rel string) ([]domain.ID, error) {
	tags := metadata.ParseTags(f.Info.Tags)
	names := naming.ParseTrack(rel)
	title := firstNonEmpty(tags.Title, names.Title)
	artistName := firstNonEmpty(tags.AlbumArtist, tags.Artist, names.Artist, domain.UnknownArtist)
	if tags.AlbumArtist == "" && tags.Compilation {
		artistName = domain.VariousArtists
	}
	albumTitle := firstNonEmpty(tags.Album, names.Album, domain.UnknownAlbum)
	disc, number, year := tags.Disc, tags.Number, tags.Year
	if number == 0 {
		disc, number = names.Disc, names.Number
	}
	if disc == 0 {
		disc = names.Disc // "CD2" folder
	}
	if year == 0 {
		year = names.Year
	}

	artist, artistCreated, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemArtist, GroupKey: "artist:" + musicKey(artistName), Title: artistName,
		SortTitle: naming.SortTitle(firstNonEmpty(tags.ArtistSort, artistName)),
	})
	if err != nil {
		return nil, err
	}
	album, _, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemAlbum, ParentID: &artist.ID,
		GroupKey: fmt.Sprintf("album:%s:%s", artist.ID, musicKey(albumTitle)), Title: albumTitle,
		SortTitle: naming.SortTitle(firstNonEmpty(tags.AlbumSort, albumTitle)), Year: year,
	})
	if err != nil {
		return nil, err
	}
	trackKey := fmt.Sprintf("track:%s:%d:%d", album.ID, disc, number)
	if number == 0 {
		trackKey = fmt.Sprintf("track:%s:%s", album.ID, musicKey(title))
	}
	track, _, err := a.findOrCreate(ctx, q, domain.Item{
		LibraryID: lib.ID, Kind: domain.ItemTrack, ParentID: &album.ID, GroupKey: trackKey, Title: title,
	})
	if err != nil {
		return nil, err
	}
	if err := q.SetTrack(ctx, domain.Track{
		ItemID: track.ID, AlbumID: album.ID, ArtistID: artist.ID, Disc: disc, Number: number,
		Artists: firstNonEmpty(tags.Artist, artistName), TrackGain: tags.TrackGain, TrackPeak: tags.TrackPeak,
		AlbumGain: tags.AlbumGain, AlbumPeak: tags.AlbumPeak,
	}); err != nil {
		return nil, err
	}
	// Everything that describes a track comes from its tags: written here, without a separate job.
	var ids map[string]string
	if id := tags.IDs["musicbrainz_recording"]; id != "" {
		ids = map[string]string{"musicbrainz_recording": id}
	}
	if _, err := q.SetMetadata(ctx, track.ID, domain.Metadata{
		Title: title, SortTitle: naming.SortTitle(firstNonEmpty(tags.TitleSort, title)), Year: year,
		PremiereDate: tags.Date, Genres: tags.Genres, Runtime: f.Info.Duration.Round(time.Second), ProviderIDs: ids,
	}, a.now()); err != nil {
		return nil, err
	}
	// Two files of the same track (FLAC and MP3) are versions told apart by their format.
	version := strings.ToUpper(strings.TrimPrefix(path.Ext(rel), "."))
	if err := q.LinkFile(ctx, track.ID, f.ID, version, 0); err != nil {
		return nil, err
	}
	enrich := []domain.ID{album.ID}
	if artistCreated {
		enrich = append(enrich, artist.ID)
	}
	return enrich, nil
}

// musicSources gathers the local metadata of an album or an artist: what the tags of its tracks
// say, its NFO (album.nfo, artist.nfo) and the images in its folders. A track has nothing to read
// again: everything that describes it comes from its tags, read at analysis time.
func (a *App) musicSources(ctx context.Context, lib domain.Library, item domain.Item) (localSources, bool, error) {
	read := a.store.Read()
	switch item.Kind {
	case domain.ItemAlbum:
		files, err := read.AlbumFiles(ctx, item.ID)
		if err != nil || len(files) == 0 {
			return localSources{}, false, err
		}
		sum, err := read.AlbumTracks(ctx, item.ID)
		if err != nil {
			return localSources{}, false, err
		}
		first, err := read.File(ctx, files[0].ID)
		if err != nil {
			return localSources{}, false, err
		}
		tags := metadata.ParseTags(first.Info.Tags)
		names := domain.Metadata{
			Title: item.Title, SortTitle: item.SortTitle, Year: sum.Year, PremiereDate: tags.Date, Genres: sum.Genres,
			Runtime: sum.Runtime.Round(time.Second), ProviderIDs: pick(tags.IDs, "musicbrainz_release", "musicbrainz_releasegroup"),
		}
		src := localSources{names: names}
		albumDirs, _ := musicDirs(lib, files[0].Path)
		if len(albumDirs) > 0 {
			// The album folder first (above a disc folder).
			for i := len(albumDirs) - 1; i >= 0; i-- {
				src.nfo = append(src.nfo, filepath.Join(albumDirs[i], "album.nfo"))
			}
			src.artwork = func() ([]metadata.Artwork, error) { return metadata.AlbumArtwork(albumDirs...) }
		}
		return src, true, nil

	case domain.ItemArtist:
		first, err := read.FirstFileOfArtist(ctx, item.ID)
		if store.IsNotFound(err) {
			return localSources{}, false, nil
		}
		if err != nil {
			return localSources{}, false, err
		}
		f, err := read.File(ctx, first.ID)
		if err != nil {
			return localSources{}, false, err
		}
		tags := metadata.ParseTags(f.Info.Tags)
		src := localSources{names: domain.Metadata{
			Title: item.Title, SortTitle: item.SortTitle, ProviderIDs: pick(tags.IDs, "musicbrainz_artist"),
		}}
		// The folder above the album is the artist's only if it bears the artist's name (and not
		// "Music", "Compilations"...).
		if _, dir := musicDirs(lib, first.Path); dir != "" && "artist:"+musicKey(filepath.Base(dir)) == item.GroupKey {
			src.nfo = []string{filepath.Join(dir, "artist.nfo")}
			src.artwork = func() ([]metadata.Artwork, error) { return metadata.ArtistArtwork(dir) }
		}
		return src, true, nil

	case domain.ItemMovie, domain.ItemSeries, domain.ItemSeason, domain.ItemEpisode, domain.ItemTrack,
		domain.ItemBookSeries, domain.ItemBook, domain.ItemPhotoAlbum, domain.ItemPhoto:
	}
	return localSources{}, false, nil
}

// pick keeps some keys of a map (nil if none).
func pick(m map[string]string, keys ...string) map[string]string {
	var out map[string]string
	for _, k := range keys {
		if v := m[k]; v != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = v
		}
	}
	return out
}

// coverCodecs are the codecs of a picture attached to an audio file (its cover) and the extension
// of the extracted file.
var coverCodecs = map[string]string{"mjpeg": ".jpg", "png": ".png"}

// embeddedCover returns the cover attached to the first track of an album that has one, extracted
// into the metadata folder (once: the name follows the file's fingerprint). Without a cover, or if
// extraction fails (logged), the album has none.
func (a *App) embeddedCover(ctx context.Context, albumID domain.ID) (wantedImage, bool) {
	read := a.store.Read()
	files, err := read.AlbumFiles(ctx, albumID)
	if err != nil {
		a.log.WarnContext(ctx, "embedded cover: unreadable tracks", "album", albumID, "err", err)
		return wantedImage{}, false
	}
	for _, ref := range files {
		f, err := read.File(ctx, ref.ID)
		if err != nil {
			continue
		}
		for _, s := range f.Info.Streams {
			ext, ok := coverCodecs[s.Codec]
			if s.Kind != domain.StreamAttachment || !ok || len(f.Fingerprint) < 8 {
				continue
			}
			id := albumID.String()
			target := filepath.Join(a.metadataDir, "images", id[:2], id, "poster-embedded-"+f.Fingerprint[:8]+ext)
			if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
				if err := a.extractCover(ctx, f.Path, s.Index, target); err != nil {
					a.log.WarnContext(ctx, "cannot extract embedded cover", "path", f.Path, "err", err)
					return wantedImage{}, false
				}
			}
			return wantedImage{kind: domain.ImagePoster, source: domain.ImageEmbedded, path: target}, true
		}
	}
	return wantedImage{}, false
}

// extractCover copies the attached picture with the given index out of a file (without re-encoding
// it) into target, atomically: never a half-written file.
func (a *App) extractCover(ctx context.Context, input string, index int, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	bin := a.ffmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	tmp := target + ".part"
	cmd := proc.Command(ctx, bin, "-hide_banner", "-nostdin", "-v", "error", "-i", "file:"+input,
		"-map", "0:"+strconv.Itoa(index), "-c", "copy", "-frames:v", "1", "-update", "1", "-f", "image2", "-y", "file:"+tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return os.Rename(tmp, target)
}

// musicDirs returns the folders of an album from the path of one of its files (the one holding the
// tracks, then the album's if that is above a "CD1" disc folder) and the folder of its artist (the
// parent of the album's, "" if that is a root). A file at the root has neither.
func musicDirs(lib domain.Library, filePath string) (albumDirs []string, artistDir string) {
	root, rel, ok := relativeTo(lib.Paths, filePath)
	if !ok {
		return nil, ""
	}
	parts := strings.Split(path.Dir(rel), "/")
	if parts[0] == "." {
		return nil, ""
	}
	dir := filepath.Join(root, filepath.FromSlash(path.Dir(rel)))
	albumDirs = []string{dir}
	if len(parts) >= 2 && naming.IsDiscDir(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
		dir = filepath.Dir(dir)
		albumDirs = append(albumDirs, dir)
	}
	if len(parts) >= 2 {
		artistDir = filepath.Dir(dir)
	}
	return albumDirs, artistDir
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
