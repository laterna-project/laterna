package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/jobs"
	"github.com/laterna-project/laterna/internal/media/transcode"
	"github.com/laterna-project/laterna/internal/media/trickplay"
	"github.com/laterna-project/laterna/internal/proc"
	"github.com/laterna-project/laterna/internal/store"
)

// Scrubbing thumbnails (trickplay): generated in the background for each video file, kept in the
// metadata folder (they take long to redo) and served at an immutable URL. A new generation has a
// new key, and the old one is deleted by the purge.

// trickplayDir is the folder of the sheets of one generation.
func (a *App) trickplayDir(fileID domain.ID, key string) string {
	id := fileID.String()
	return filepath.Join(a.metadataDir, "trickplay", id[:2], id, key)
}

// generateTrickplay produces the scrubbing thumbnails of a file, unless they already exist for its
// current content. A file without video is recorded as such: nothing to redo.
func (a *App) generateTrickplay(ctx context.Context, target string) error {
	id, err := domain.ParseID(target)
	if err != nil {
		return jobs.Permanent(err)
	}
	read := a.store.Read()
	f, err := read.File(ctx, id)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if f.MissingSince != nil || f.AnalyzedAt == nil || !a.Settings().Trickplay {
		return nil
	}
	if t, err := read.Trickplay(ctx, id); err == nil && t.Fingerprint == f.Fingerprint {
		return nil
	} else if err != nil && !store.IsNotFound(err) {
		return err
	}
	spec := trickplay.Default
	record := domain.Trickplay{
		FileID: id, Fingerprint: f.Fingerprint, Interval: spec.Interval, Columns: spec.Columns, Rows: spec.Rows, CreatedAt: a.now(),
	}
	if hasVideo(f.Info) {
		key := randomKey()
		dir := a.trickplayDir(id, key)
		res, err := a.runTrickplay(ctx, f, spec, dir)
		if err != nil {
			_ = os.RemoveAll(dir)
			return err
		}
		record.Key, record.Width, record.Height, record.Count, record.Sheets = key, res.Width, res.Height, res.Count, res.Sheets
	}
	if err := a.store.Write(ctx, func(q store.Q) error { return q.SetTrickplay(ctx, record) }); err != nil {
		return err
	}
	a.log.DebugContext(ctx, "preview thumbnails ready", "path", f.Path, "count", record.Count, "sheets", record.Sheets)
	return nil
}

// runTrickplay runs FFmpeg, then assembles the sheets in dir.
func (a *App) runTrickplay(ctx context.Context, f domain.MediaFile, spec trickplay.Spec, dir string) (trickplay.Result, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return trickplay.Result{}, err
	}
	bin := a.ffmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := proc.Command(ctx, bin, trickplay.Args(f.Path, spec, a.trickplayToneMap(f.Info), dir)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return trickplay.Result{}, ctx.Err()
		}
		return trickplay.Result{}, jobs.Permanent(fmt.Errorf("trickplay: %w: %s", err, strings.TrimSpace(stderr.String())))
	}
	res, err := trickplay.Sheets(dir, spec, f.Info.Duration)
	if err != nil {
		return trickplay.Result{}, jobs.Permanent(err)
	}
	return res, nil
}

// trickplayToneMap returns the HDR to SDR conversion for the thumbnails of an HDR video: zscale, on
// the CPU, if the startup test validated it (thumbnails are small); "" otherwise.
func (a *App) trickplayToneMap(info domain.MediaInfo) string {
	hdr := slices.ContainsFunc(info.Streams, func(s domain.Stream) bool {
		return s.Kind == domain.StreamVideo && s.DynamicRange != "" && s.DynamicRange != domain.SDR
	})
	if !hdr {
		return ""
	}
	caps, err := a.encoders()
	if err != nil || !slices.ContainsFunc(caps.ToneMappers, func(t transcode.ToneMapper) bool { return t.Name == "zscale" }) {
		return ""
	}
	tm, _ := transcode.ToneMapperByName("zscale")
	return tm.Filter()
}

func randomKey() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never returns an error (crypto/rand)
	return hex.EncodeToString(b)
}

// trickplayOf returns the ready scrubbing thumbnails of a file, nil otherwise.
func (a *App) trickplayOf(ctx context.Context, f domain.MediaFile) (*domain.Trickplay, error) {
	t, err := a.store.Read().Trickplay(ctx, f.ID)
	if store.IsNotFound(err) {
		return nil, nil //nolint:nilnil // absence on purpose: not generated yet
	}
	if err != nil || t.Key == "" || t.Fingerprint != f.Fingerprint {
		return nil, err
	}
	return &t, nil
}

// TrickplaySheet returns the path of a sheet (not found if the key is stale).
func (a *App) TrickplaySheet(ctx context.Context, fileID domain.ID, key string, n int) (string, error) {
	t, err := a.store.Read().Trickplay(ctx, fileID)
	if store.IsNotFound(err) || (err == nil && (t.Key == "" || t.Key != key || n < 0 || n >= t.Sheets)) {
		return "", domain.NotFound("trickplay.not_found")
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(a.trickplayDir(fileID, key), trickplay.SheetName(n)), nil
}

// purgeTrickplay deletes the sheets that are no longer needed: replaced generations, forgotten
// files. A recent folder is spared: its generation may be under way.
func (a *App) purgeTrickplay(ctx context.Context, root *os.Root) (int, error) {
	keys, err := a.store.Read().TrickplayKeys(ctx)
	if err != nil {
		return 0, err
	}
	used := map[string]bool{}
	for id, key := range keys {
		s := id.String()
		used[filepath.ToSlash(filepath.Join("trickplay", s[:2], s, key))] = true
	}
	cutoff := time.Now().Add(-time.Hour)
	removed := 0
	err = fs.WalkDir(root.FS(), "trickplay", func(p string, d fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return err
		case !d.IsDir() || strings.Count(p, "/") != 3:
			return nil
		}
		if used[p] {
			return fs.SkipDir
		}
		if info, err := d.Info(); err != nil || info.ModTime().After(cutoff) {
			return fs.SkipDir // folder gone in the meantime, or too recent
		}
		if root.RemoveAll(p) == nil {
			removed++
		}
		return fs.SkipDir
	})
	return removed, err
}
