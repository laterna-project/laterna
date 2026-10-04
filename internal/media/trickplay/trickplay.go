// Package trickplay prepares the scrubbing thumbnails of a video. FFmpeg only decodes keyframes
// (fast even in 4K) and writes them scaled down, with their time in their name. Each position of
// the grid (0, Interval, 2 x Interval...) takes the nearest keyframe, and the thumbnails are
// assembled into JPEG sheets that the client slices up.
package trickplay

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Spec configures the thumbnails: one every Interval, Width pixels wide, laid out on sheets of
// Columns x Rows.
type Spec struct {
	Interval      time.Duration
	Width         int
	Columns, Rows int
}

// Default is one thumbnail every 10 s, 320 pixels wide, on 10 x 10 sheets (16 min 40 s of video per
// sheet).
var Default = Spec{Interval: 10 * time.Second, Width: 320, Columns: 10, Rows: 10}

// Result describes the sheets produced.
type Result struct {
	// Count is the number of thumbnails. The last sheet may not be full.
	Count int
	// Width and Height of one thumbnail.
	Width, Height int
	// Sheets is the number of sheets, named SheetName(0...Sheets-1).
	Sheets int
}

// SheetName is the name of sheet n.
func SheetName(n int) string { return fmt.Sprintf("%03d.jpg", n) }

// rankStep separates ranks in the name of a keyframe: 10^9 ms (eleven and a half days), far longer
// than any video.
const rankStep = 1_000_000_000

// Args returns the FFmpeg command that writes the keyframes of input into dir, scaled to
// spec.Width. toneMap is an HDR to SDR filter to apply after scaling ("" for none).
//
// Keyframes do not always come out in order (open GOPs) and the encoder refuses a timestamp that
// goes backwards, so each one is named rank x 10^9 + time, in milliseconds ("10417.jpg" then
// "1000000000.jpg" for 10.417 s then 0 s). Sheets works the time back out.
func Args(input string, spec Spec, toneMap, dir string) []string {
	vf := fmt.Sprintf("scale=%d:-2:flags=bilinear", spec.Width)
	if toneMap != "" {
		vf += "," + toneMap
	}
	vf += fmt.Sprintf(",setpts=N*%d/TB+PTS,format=yuvj420p", rankStep/1000)
	return []string{
		"-hide_banner", "-nostdin", "-v", "error", "-skip_frame", "nokey",
		"-i", "file:" + input, "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", vf,
		"-fps_mode", "passthrough", "-enc_time_base", "1:1000", "-frame_pts", "1",
		"-q:v", "4", "-f", "image2", "file:" + filepath.Join(dir, "%d.jpg"),
	}
}

// keyframe is a keyframe written by FFmpeg.
type keyframe struct {
	at   time.Duration
	name string
}

// Sheets assembles the keyframes in dir into sheets (JPEG quality 80) covering duration (0 means up
// to the last keyframe), then deletes them. Thumbnail n shows the keyframe nearest to n x
// spec.Interval.
func Sheets(dir string, spec Spec, duration time.Duration) (Result, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Result{}, err
	}
	var keys []keyframe
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jpg")
		n, err := strconv.ParseInt(name, 10, 64)
		if e.IsDir() || !ok || err != nil || n < -rankStep/2 {
			continue
		}
		// Nearest rank: a slightly negative time (MP4 edit list) can still be read.
		ms := n - (n+rankStep/2)/rankStep*rankStep
		keys = append(keys, keyframe{at: time.Duration(ms) * time.Millisecond, name: e.Name()})
	}
	if len(keys) == 0 {
		return Result{}, errors.New("trickplay: no keyframe")
	}
	slices.SortFunc(keys, func(a, b keyframe) int { return int(a.at - b.at) })
	if duration <= 0 {
		duration = keys[len(keys)-1].at + time.Millisecond
	}
	count := max(1, int((duration+spec.Interval-1)/spec.Interval))
	// Keyframe of each thumbnail: the one nearest to its position.
	chosen := make([]int, count)
	k := 0
	for n := range count {
		at := time.Duration(n) * spec.Interval
		for k+1 < len(keys) && absDiff(keys[k+1].at, at) <= absDiff(keys[k].at, at) {
			k++
		}
		chosen[n] = k
	}
	decoded := map[int]image.Image{}
	get := func(i int) (image.Image, error) {
		if img, ok := decoded[i]; ok {
			return img, nil
		}
		img, err := decode(filepath.Join(dir, keys[i].name))
		if err == nil {
			decoded[i] = img
		}
		return img, err
	}
	first, err := get(chosen[0])
	if err != nil {
		return Result{}, err
	}
	res := Result{Count: count, Width: first.Bounds().Dx(), Height: first.Bounds().Dy()}
	per := spec.Columns * spec.Rows
	for start := 0; start < count; start += per {
		n := min(per, count-start)
		// The sheet is only as big as what it holds: no useless black margin.
		cols, rows := min(spec.Columns, n), (n+spec.Columns-1)/spec.Columns
		sheet := image.NewRGBA(image.Rect(0, 0, cols*res.Width, rows*res.Height))
		for i := range n {
			img, err := get(chosen[start+i])
			if err != nil {
				return Result{}, err
			}
			x, y := i%spec.Columns*res.Width, i/spec.Columns*res.Height
			draw.Draw(sheet, image.Rect(x, y, x+res.Width, y+res.Height), img, img.Bounds().Min, draw.Src)
		}
		if err := encode(filepath.Join(dir, SheetName(res.Sheets)), sheet); err != nil {
			return Result{}, err
		}
		res.Sheets++
		// Later sheets never go back to earlier keyframes, so their memory is released.
		for i := range decoded {
			if i < chosen[start+n-1] {
				delete(decoded, i)
			}
		}
	}
	for _, key := range keys {
		if err := os.Remove(filepath.Join(dir, key.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Result{}, err
		}
	}
	return res, nil
}

func absDiff(a, b time.Duration) time.Duration {
	if a > b {
		return a - b
	}
	return b - a
}

func decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("trickplay: %s: %w", filepath.Base(path), err)
	}
	return img, nil
}

func encode(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 80}); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
