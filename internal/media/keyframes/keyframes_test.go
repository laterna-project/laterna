package keyframes

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

// The Cues of a Matroska file give the same keyframes as ffprobe, without reading the file.
func TestMatroskaCuesMatchProbe(t *testing.T) {
	_, ffprobe, _ := testfixtures.FFmpeg()
	ctx := context.Background()
	for _, rel := range []string{"Movies/Dual Audio (2019)/Dual Audio (2019).mkv", "Movies/HDR Test (2021)/HDR Test (2021).mkv"} {
		path := testfixtures.Path(t, rel)
		cues, err := Matroska(path)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		probed, err := Probe(ctx, ffprobe, path)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cues, probed) {
			t.Errorf("%s: Cues %v, ffprobe %v", rel, cues, probed)
		}
	}
}

func TestNoIndexFallsBackToProbe(t *testing.T) {
	_, ffprobe, _ := testfixtures.FFmpeg()
	for _, rel := range []string{"Movies/Stream Dump (2018)/Stream Dump (2018).mkv", "Movies/Big Test Movie (2020)/Big Test Movie (2020).mp4"} {
		path := testfixtures.Path(t, rel)
		if _, err := Matroska(path); !errors.Is(err, ErrNoIndex) {
			t.Errorf("%s: %v, want ErrNoIndex", rel, err)
		}
		times, err := Read(context.Background(), ffprobe, path)
		if err != nil || len(times) != 6 || times[1] != 2*time.Second {
			t.Errorf("%s: %v %v", rel, times, err)
		}
	}
}

func TestMatroskaRejectsGarbage(t *testing.T) {
	path := t.TempDir() + "/fake.mkv"
	if err := os.WriteFile(path, []byte("not a matroska file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Matroska(path); !errors.Is(err, ErrNoIndex) {
		t.Errorf("%v", err)
	}
}

// Real file (LATERNA_KEYFRAMES_REAL): Cues against ffprobe on a real episode.
func TestRealFile(t *testing.T) {
	path := os.Getenv("LATERNA_KEYFRAMES_REAL")
	if path == "" {
		t.Skip("LATERNA_KEYFRAMES_REAL is not set")
	}
	_, ffprobe, _ := testfixtures.FFmpeg()
	start := time.Now()
	cues, err := Matroska(path)
	t.Logf("Cues: %d keyframes in %v (%v)", len(cues), time.Since(start), err)
	start = time.Now()
	probed, err := Probe(context.Background(), ffprobe, path)
	t.Logf("ffprobe: %d keyframes in %v (%v)", len(probed), time.Since(start), err)
	if cues != nil && !slices.Equal(cues, probed) {
		t.Errorf("mismatch: %d against %d", len(cues), len(probed))
	}
}
