package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/media/transcode"
	"github.com/laterna-project/laterna/internal/proc"
)

// Audio conversions: a file the device cannot play is converted once to AAC, in the cache, then
// served as a plain file. The name follows the file's fingerprint and the converted stream, so a
// changed file is converted again.

// convertedKeep: a conversion that has not been used for this long is deleted by the purge.
const convertedKeep = 30 * 24 * time.Hour

// conversion is a conversion in progress or finished: done is closed at the end and err says how it
// went.
type conversion struct {
	path string
	done chan struct{}
	err  error
}

// conversions holds the conversions in progress, by output file: two playbacks of the same file
// wait for the same conversion.
type conversions struct {
	mu     sync.Mutex
	byPath map[string]*conversion
}

// convertedPath is the converted file for an audio stream of a file at a bitrate (kbit/s; 0 means
// the playback one).
func (a *App) convertedPath(f domain.MediaFile, audio, rate int) string {
	fp := f.Fingerprint
	if len(fp) < 2 {
		fp = f.ID.String()
	}
	name := fp + "-" + strconv.Itoa(audio) + "-aac"
	if rate > 0 {
		name += strconv.Itoa(rate)
	}
	return filepath.Join(a.cacheDir, "converted", fp[:2], name+".m4a")
}

// convertAudio returns the conversion of an audio stream at a bitrate (kbit/s; 0 means the playback
// one): already done (the file is kept for one more period), in progress, or started now. It goes
// on if the requester leaves, since another playback will use it.
func (a *App) convertAudio(f domain.MediaFile, audio, channels, rate int) *conversion {
	path := a.convertedPath(f, audio, rate)
	a.convs.mu.Lock()
	defer a.convs.mu.Unlock()
	if c := a.convs.byPath[path]; c != nil {
		return c
	}
	c := &conversion{path: path, done: make(chan struct{})}
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now) // served again: kept for one more period
		close(c.done)
		return c
	}
	a.convs.byPath[path] = c
	parent := a.runCtx
	if parent == nil {
		parent = context.Background()
	}
	a.background.Go(func() {
		start := time.Now()
		c.err = a.runConversion(parent, f, audio, channels, rate, path)
		if c.err == nil {
			a.log.InfoContext(parent, "audio conversion ready", "path", f.Path, "duration", time.Since(start).Round(time.Millisecond))
		} else {
			a.log.WarnContext(parent, "audio conversion failed", "path", f.Path, "err", c.err)
		}
		a.convs.mu.Lock()
		delete(a.convs.byPath, path)
		a.convs.mu.Unlock()
		close(c.done)
	})
	return c
}

// runConversion runs FFmpeg into a temporary file that is renamed at the end, so there is never a
// half-converted file.
func (a *App) runConversion(ctx context.Context, f domain.MediaFile, audio, channels, rate int, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	bin := a.ffmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	tmp := path + ".part"
	cmd := proc.Command(ctx, bin, transcode.AudioArgs(f.Path, audio, channels, rate, tmp)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return os.Rename(tmp, path)
}

// awaitConversion waits for a conversion to end (limit at most).
func awaitConversion(ctx context.Context, c *conversion, limit time.Duration) (string, error) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-c.done:
		if c.err != nil {
			return "", fmt.Errorf("audio conversion: %w", c.err)
		}
		return c.path, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", errors.New("audio conversion took too long")
	}
}

// purgeConverted deletes the conversions that have not been used for convertedKeep, and temporary
// files left behind (hard stop during a conversion).
func (a *App) purgeConverted(ctx context.Context) {
	root, err := os.OpenRoot(a.cacheDir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		a.log.WarnContext(ctx, "conversions: unreadable cache", "err", err)
		return
	}
	defer func() { _ = root.Close() }()
	a.convs.mu.Lock()
	running := make(map[string]bool, len(a.convs.byPath))
	for p := range a.convs.byPath {
		running[p+".part"] = true
	}
	a.convs.mu.Unlock()
	cutoff := time.Now().Add(-convertedKeep)
	removed := 0
	_ = fs.WalkDir(root.FS(), "converted", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || running[filepath.Join(a.cacheDir, filepath.FromSlash(p))] {
			return nil //nolint:nilerr // missing or unreadable folder: nothing to purge
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // file gone in the meantime
		}
		if info.ModTime().Before(cutoff) || (strings.HasSuffix(p, ".part") && info.ModTime().Before(time.Now().Add(-time.Hour))) {
			if root.Remove(p) == nil {
				removed++
			}
		}
		return nil
	})
	if removed > 0 {
		a.log.InfoContext(ctx, "audio conversions: purge", "files", removed)
	}
}
