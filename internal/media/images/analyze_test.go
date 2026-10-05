package images

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

func gradient(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 120, A: 255})
		}
	}
	return img
}

func save(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if filepath.Ext(path) == ".png" {
		err = png.Encode(f, img)
	} else {
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: 90})
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnalyze(t *testing.T) {
	dir := t.TempDir()
	save(t, filepath.Join(dir, "a.jpg"), gradient(400, 600))
	save(t, filepath.Join(dir, "b.png"), gradient(400, 600))
	save(t, filepath.Join(dir, "c.jpg"), gradient(1280, 720))

	a, err := Analyze(filepath.Join(dir, "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Width != 400 || a.Height != 600 || len(a.BlurHash) < 20 || len(a.Hash) != 16 {
		t.Errorf("analysis: %+v", a)
	}
	b, err := Analyze(filepath.Join(dir, "b.png"))
	if err != nil || b.Hash == a.Hash {
		t.Errorf("PNG: %+v %v (different content, different hash)", b, err)
	}
	again, _ := Analyze(filepath.Join(dir, "a.jpg"))
	if again != a {
		t.Error("analysis is not deterministic")
	}
	c, _ := Analyze(filepath.Join(dir, "c.jpg"))
	if c.Width != 1280 || c.Height != 720 {
		t.Errorf("landscape: %+v", c)
	}
}

func TestAnalyzeRejects(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "fake.jpg")
	if err := os.WriteFile(bad, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(bad); err == nil {
		t.Error("invalid file accepted")
	}
	if _, err := Analyze(filepath.Join(dir, "absent.jpg")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestAnalyzeFixturePoster(t *testing.T) {
	a, err := Analyze(testfixtures.Path(t, "Movies/Big Test Movie (2020)/poster.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Width != 400 || a.Height != 600 || a.BlurHash == "" {
		t.Errorf("fixture poster: %+v", a)
	}
}
