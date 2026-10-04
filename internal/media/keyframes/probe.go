package keyframes

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/proc"
)

// Probe reads the keyframes of the first video stream (cover art excluded) with ffprobe, going
// through the packets of the whole file. This is slow on a big file and belongs in a background
// job.
func Probe(ctx context.Context, ffprobe, path string) ([]time.Duration, error) {
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	cmd := proc.Command(ctx, ffprobe, "-v", "error", "-select_streams", "V:0",
		"-show_entries", "packet=pts_time,flags", "-of", "csv=p=0", "file:"+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe (keyframes): %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var times []time.Duration
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		pts, flags, ok := strings.Cut(sc.Text(), ",")
		if !ok || !strings.HasPrefix(flags, "K") || pts == "N/A" {
			continue
		}
		s, err := strconv.ParseFloat(pts, 64)
		if err != nil {
			continue
		}
		times = append(times, time.Duration(s*float64(time.Second)).Round(time.Microsecond))
	}
	if len(times) == 0 {
		return nil, errors.New("ffprobe: no video keyframe")
	}
	slices.Sort(times)
	return slices.Compact(times), nil
}

// Read returns the index of a file: the Cues if it is a Matroska file that has them, ffprobe
// otherwise.
func Read(ctx context.Context, ffprobe, path string) ([]time.Duration, error) {
	times, err := Matroska(path)
	if errors.Is(err, ErrNoIndex) {
		return Probe(ctx, ffprobe, path)
	}
	return times, err
}
