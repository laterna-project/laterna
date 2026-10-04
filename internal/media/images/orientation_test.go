package images

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

// A photo stored sideways (64 x 48, left half red, right half blue) with orientation 6: as
// displayed it is 48 x 64 with red on top.
func TestOrientation(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			c := color.RGBA{R: 220, A: 255}
			if x >= 32 {
				c = color.RGBA{B: 220, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "couchee.jpg")
	if err := os.WriteFile(src, testfixtures.JPEGWithExif(img, testfixtures.Exif{Orientation: 6}), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(src)
	if err != nil || a.Width != 48 || a.Height != 64 {
		t.Fatalf("analysis: %+v %v", a, err)
	}
	dst := filepath.Join(dir, "reduite.jpg")
	if err := Resize(src, dst, 24); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := out.Bounds(); b.Dx() != 24 || b.Dy() != 32 {
		t.Fatalf("resized version: %v", b)
	}
	red := func(x, y int) bool { r, _, bl, _ := out.At(x, y).RGBA(); return r > bl }
	if !red(12, 4) || red(12, 28) {
		t.Error("orientation not applied: red should be on top")
	}
	// All the quarter turns and flips: dimensions are swapped from 5 up.
	for o := 1; o <= 8; o++ {
		b := orient(img, o).Bounds()
		if want := (o >= 5); (b.Dx() == 48) != want {
			t.Errorf("orientation %d: %v", o, b)
		}
	}
}
