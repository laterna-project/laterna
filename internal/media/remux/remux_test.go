package remux

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/media/fmp4"
	"github.com/laterna-project/laterna/internal/media/keyframes"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

// collect gathers the init segment and the fragments of a whole run.
func collect(t *testing.T, ffmpeg string, o Options) ([]byte, []fmp4.Fragment) {
	t.Helper()
	var init []byte
	var frags []fmp4.Fragment
	err := fmp4.Run(context.Background(), fmp4.Command{Bin: ffmpeg, Args: Args(o)},
		func(b []byte) error { init = b; return nil },
		func(f fmp4.Fragment) error { frags = append(frags, f); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return init, frags
}

func TestRunsAreReproducible(t *testing.T) {
	path := testfixtures.Path(t, "Films/Deux Pistes (2019)/Deux Pistes (2019).mkv")
	ffmpeg, ffprobe, _ := testfixtures.FFmpeg()
	keys, err := keyframes.Read(context.Background(), ffprobe, path)
	if err != nil {
		t.Fatal(err)
	}

	init, frags := collect(t, ffmpeg, Options{Path: path, Audio: 1})
	var starts []time.Duration
	for _, f := range frags {
		starts = append(starts, f.Start)
	}
	// One fragment per keyframe, dated by the presentation time of the keyframe.
	if !slices.Equal(starts, keys) {
		t.Fatalf("fragment starts %v, keyframes %v", starts, keys)
	}

	// A run started by a seek to the 4th keyframe: same init segment, same fragments (content and
	// times), earlier fragments aside. The header may differ by a fraction of a millisecond for
	// audio: a continuous run adds up the exact frame durations, while a run started by a seek
	// starts again from the Matroska timestamp, which is rounded to the millisecond.
	jumpInit, jump := collect(t, ffmpeg, Options{Path: path, Start: keys[3], Audio: 1})
	if !bytes.Equal(init, jumpInit) {
		t.Error("different init segment after a seek")
	}
	jump = slices.DeleteFunc(jump, func(f fmp4.Fragment) bool { return f.Start < keys[3] })
	if len(jump) != len(frags)-3 {
		t.Fatalf("%d fragments after the seek, want %d", len(jump), len(frags)-3)
	}
	media := func(f fmp4.Fragment) []byte { return f.Data[binary.BigEndian.Uint32(f.Data[:4]):] }
	for i, f := range jump {
		if a := frags[i+3]; a.Start != f.Start || !bytes.Equal(media(a), media(f)) {
			t.Errorf("fragment %d differs between runs", i+3)
		}
	}
}

func TestArgs(t *testing.T) {
	args := strings.Join(Args(Options{Path: `C:\films\a.mkv`, Start: 6256 * time.Millisecond, Audio: 2, VideoTag: "hvc1"}), " ")
	for _, want := range []string{"-ss 6.256000", `-i file:C:\films\a.mkv`, "-map 0:V:0 -map 0:2", "-tag:v hvc1", "-map_chapters -1", "negative_cts_offsets", "pipe:1"} {
		if !strings.Contains(args, want) {
			t.Errorf("arguments without %q: %s", want, args)
		}
	}
	if args := strings.Join(Args(Options{Audio: -1}), " "); strings.Contains(args, "-map 0:-1") || strings.Contains(args, "-tag:v") {
		t.Errorf("no audio and no tag: %s", args)
	}
}
