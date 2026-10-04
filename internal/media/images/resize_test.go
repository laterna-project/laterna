package images

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func TestResize(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "poster.jpg")
	save(t, src, gradient(600, 900))
	dst := filepath.Join(dir, "cache", "ab", "poster-240")
	if err := Resize(src, dst, 240); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil || format != "jpeg" || cfg.Width != 240 || cfg.Height != 360 {
		t.Errorf("resized version: %s %d×%d %v", format, cfg.Width, cfg.Height, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 1 {
		t.Errorf("temporary file left behind: %v", entries)
	}

	// Upscaling is refused: the original is served instead.
	if err := Resize(src, dst, 600); err == nil {
		t.Error("upscaling accepted")
	}
}

func TestResizeKeepsTransparency(t *testing.T) {
	dir := t.TempDir()
	logo := image.NewNRGBA(image.Rect(0, 0, 400, 100))
	logo.Set(10, 10, color.NRGBA{R: 255, A: 128})
	src := filepath.Join(dir, "logo.png")
	save(t, src, logo)
	dst := filepath.Join(dir, "logo-160")
	if err := Resize(src, dst, 160); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, format, err := image.DecodeConfig(f); err != nil || format != "png" {
		t.Errorf("transparent logo: %s %v (want PNG)", format, err)
	}
}
