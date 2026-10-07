package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/metadata"
	"github.com/laterna-project/laterna/internal/proc"
)

// Episode stills: an episode with no thumbnail of its own, next to its file or given by its NFO,
// shows a frame of its video, so that a list of episodes is not a row of blanks. That is the case
// of an episode just aired, before the TV databases have a picture of it. The frame is the last
// resort: a thumbnail that appears later (Sonarr writing it, or an NFO giving its URL) replaces it,
// and the purge deletes the frame.

// stillAt is where the frame is taken, as a share of the runtime: past the recap, the cold open
// and the opening titles of most episodes.
const stillAt = 0.2

// stillWidth is the width of the frame at most: enough for the largest landscape card.
const stillWidth = 1280

// needsStill reports whether an episode has no thumbnail other than a frame of its video: none
// next to its file, and none the NFO gives a URL for that the server may download.
func (a *App) needsStill(local []metadata.Artwork, nfo *metadata.NFO) bool {
	if slices.ContainsFunc(local, func(aw metadata.Artwork) bool { return aw.Kind == domain.ImageThumb }) {
		return false
	}
	return nfo == nil || nfo.Images[domain.ImageThumb] == "" || !a.Settings().DownloadImages
}

// episodeStill returns a frame of the episode's video as its thumbnail, extracted once per content
// of the file (fingerprint) into the metadata folder. A file not analyzed yet has none: its
// analysis reads the metadata again.
func (a *App) episodeStill(ctx context.Context, itemID domain.ID, f domain.MediaFile) (wantedImage, bool) {
	if f.AnalyzedAt == nil || f.MissingSince != nil || f.Info.Duration <= 0 || !hasVideo(f.Info) || len(f.Fingerprint) < 8 {
		return wantedImage{}, false
	}
	id := itemID.String()
	target := filepath.Join(a.metadataDir, "images", id[:2], id, "thumb-still-"+f.Fingerprint[:8]+".jpg")
	if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
		at := time.Duration(float64(f.Info.Duration) * stillAt)
		if err := a.extractStill(ctx, f, at, target); err != nil {
			a.log.WarnContext(ctx, "cannot take a frame of the episode", "path", f.Path, "err", err)
			return wantedImage{}, false
		}
	}
	return wantedImage{kind: domain.ImageThumb, source: domain.ImageEmbedded, path: target}, true
}

// extractStill writes the frame of f at the given time into target, as JPEG, atomically. Among the
// 50 frames from there on (two seconds), FFmpeg keeps the most representative one: not a black
// frame or a blurred cut. HDR is converted to SDR like the scrubbing thumbnails.
func (a *App) extractStill(ctx context.Context, f domain.MediaFile, at time.Duration, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	bin := a.ffmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	vf := "thumbnail=50,scale='min(" + strconv.Itoa(stillWidth) + ",iw)':-2"
	if tm := a.trickplayToneMap(f.Info); tm != "" {
		vf += "," + tm
	}
	vf += ",format=yuvj420p"
	tmp := target + ".part"
	cmd := proc.Command(ctx, bin, "-hide_banner", "-nostdin", "-v", "error",
		"-ss", strconv.FormatFloat(at.Seconds(), 'f', 3, 64), "-i", "file:"+f.Path,
		"-map", "0:v:0", "-an", "-sn", "-dn", "-vf", vf,
		"-frames:v", "1", "-q:v", "3", "-update", "1", "-f", "image2", "-y", "file:"+tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return os.Rename(tmp, target)
}
