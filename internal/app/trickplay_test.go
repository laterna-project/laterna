package app

import (
	"context"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestTrickplay(t *testing.T) {
	a, _, p, movies := moviesByTitle(t)
	ctx := context.Background()
	// Regular video, and HDR video (thumbnails converted to SDR).
	for _, title := range []string{"Big Test Movie", "HDR Test"} {
		_, d, err := a.Movie(ctx, p, movies[title].ID)
		mustNil(t, err)
		if len(d.Files) == 0 || d.Files[0].Trickplay == nil {
			t.Fatalf("%s: no scrubbing thumbnails", title)
		}
		tp := d.Files[0].Trickplay
		// 12 s of video, one thumbnail every 10 s: 0 and 10 s.
		if tp.Count != 2 || tp.Sheets != 1 || tp.Width != 320 || tp.Interval != 10*time.Second || tp.Columns != 10 {
			t.Errorf("%s: %+v", title, tp)
		}
		path, err := a.TrickplaySheet(ctx, tp.FileID, tp.Key, 0)
		mustNil(t, err)
		f, err := os.Open(path)
		mustNil(t, err)
		cfg, err := jpeg.DecodeConfig(f)
		_ = f.Close()
		if err != nil || cfg.Width != min(tp.Count, tp.Columns)*tp.Width || cfg.Height != tp.Height {
			t.Errorf("%s: sheet %+v %v", title, cfg, err)
		}
		if _, err := a.TrickplaySheet(ctx, tp.FileID, "other-key", 0); !isKind(err, domain.ErrNotFound) {
			t.Errorf("stale key: %v", err)
		}
		if _, err := a.TrickplaySheet(ctx, tp.FileID, tp.Key, tp.Sheets); !isKind(err, domain.ErrNotFound) {
			t.Errorf("sheet past the last one: %v", err)
		}
	}

	// An old generation that was replaced is deleted by the purge; the current one stays.
	_, d, err := a.Movie(ctx, p, movies["Big Test Movie"].ID)
	mustNil(t, err)
	current := d.Files[0].Trickplay
	stale := a.trickplayDir(current.FileID, "old")
	mustNil(t, os.MkdirAll(stale, 0o750))
	writeText(t, filepath.Join(stale, "000.jpg"), "x")
	old := time.Now().Add(-2 * time.Hour)
	mustNil(t, os.Chtimes(stale, old, old))
	mustNil(t, a.purgeMetadata(ctx, ""))
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("old generation kept: %v", err)
	}
	if _, err := a.TrickplaySheet(ctx, current.FileID, current.Key, 0); err != nil {
		t.Errorf("current generation: %v", err)
	}
	if _, err := os.Stat(a.trickplayDir(current.FileID, current.Key)); err != nil {
		t.Errorf("current sheets deleted: %v", err)
	}
}
