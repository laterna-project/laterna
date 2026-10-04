package trickplay

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/proc"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// writeKeyframe writes, as FFmpeg would, the rank-th keyframe output: 32 x 18, plain gray, at time
// at.
func writeKeyframe(t *testing.T, dir string, rank int64, at time.Duration, gray uint8) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 32, 18))
	for i := range img.Pix {
		img.Pix[i] = gray
	}
	f, err := os.Create(filepath.Join(dir, strconv.FormatInt(rank*rankStep+at.Milliseconds(), 10)+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// grayAt reads the gray level at the center of thumbnail (col, row) of a sheet.
func grayAt(t *testing.T, path string, col, row int) uint8 {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := color.GrayModel.Convert(img.At(col*32+16, row*18+9)).(color.Gray)
	return c.Y
}

func TestSheets(t *testing.T) {
	dir := t.TempDir()
	// Keyframes output out of order (open GOPs) and unevenly spaced, the first one slightly before
	// 0 (MP4 edit list).
	writeKeyframe(t, dir, 0, 10417*time.Millisecond, 200)
	writeKeyframe(t, dir, 1, -42*time.Millisecond, 0)
	writeKeyframe(t, dir, 2, 21*time.Second, 100)
	spec := Spec{Interval: 5 * time.Second, Columns: 2, Rows: 2}
	res, err := Sheets(dir, spec, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// 30 s, one thumbnail every 5 s: 6 thumbnails on two sheets (4 + 2).
	if res != (Result{Count: 6, Width: 32, Height: 18, Sheets: 2}) {
		t.Fatalf("result: %+v", res)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"000.jpg", "001.jpg"}) {
		t.Errorf("files left: %v", names)
	}
	// Positions 0, 5, 10, 15, 20, 25 s -> keyframes 0, 0 (nearer than 10.4), 10.4, 10.4, 21, 21.
	want := []uint8{0, 0, 200, 200, 100, 100}
	for i, w := range want {
		sheet := filepath.Join(dir, SheetName(i/4))
		if got := grayAt(t, sheet, i%4%2, i%4/2); max(got, w)-min(got, w) > 8 {
			t.Errorf("thumbnail %d: gray %d, want %d", i, got, w)
		}
	}
	// Last sheet: two thumbnails, so a single row.
	f, _ := os.Open(filepath.Join(dir, "001.jpg"))
	defer func() { _ = f.Close() }()
	if cfg, err := jpeg.DecodeConfig(f); err != nil || cfg.Width != 64 || cfg.Height != 18 {
		t.Errorf("last sheet: %+v %v", cfg, err)
	}

	// Unknown duration: up to the last keyframe.
	dir = t.TempDir()
	writeKeyframe(t, dir, 0, 0, 0)
	writeKeyframe(t, dir, 1, 12*time.Second, 0)
	if res, err := Sheets(dir, spec, 0); err != nil || res.Count != 3 {
		t.Errorf("unknown duration: %+v %v", res, err)
	}
	if _, err := Sheets(t.TempDir(), Default, time.Minute); err == nil {
		t.Error("no keyframe: want an error")
	}
}

// Thumbnails of real videos, including an open-GOP 10-bit HEVC whose keyframes come out of order:
// the whole duration is covered.
func TestArgsOnFixtures(t *testing.T) {
	root := testfixtures.Library(t)
	ffmpeg, _, _ := testfixtures.FFmpeg()
	spec := Spec{Interval: 2 * time.Second, Width: 160, Columns: 3, Rows: 2}
	for _, name := range []string{"Big Test Movie (2020)/Big Test Movie (2020).mp4", "HDR Test (2021)/HDR Test (2021).mkv"} {
		dir := t.TempDir()
		input := filepath.Join(root, "Films", filepath.FromSlash(name))
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		out, err := proc.Command(ctx, ffmpeg, Args(input, spec, "", dir)...).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		res, err := Sheets(dir, spec, 12*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Count != 6 || res.Width != 160 || res.Height != 90 || res.Sheets != 1 {
			t.Errorf("%s: %+v", name, res)
		}
	}
}
