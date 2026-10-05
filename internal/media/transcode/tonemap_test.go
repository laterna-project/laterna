package transcode

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

// Each HDR to SDR conversion usable here turns the HDR10 fixture (10-bit HEVC, PQ) into 8-bit H.264
// tagged BT.709 that plays from start to end. zscale, on the CPU, is always there (GPL builds) and
// always last.
func TestToneMappers(t *testing.T) {
	ffmpeg, caps := detect(t)
	enc, _ := caps.Best()
	tms := DetectToneMappers(context.Background(), ffmpeg, enc)
	var names []string
	for _, tm := range tms {
		names = append(names, tm.Name)
	}
	t.Logf("HDR to SDR conversions usable with %s: %v", enc.Name, names)
	if len(names) == 0 || names[len(names)-1] != "zscale" {
		t.Fatalf("zscale missing or not last: %v", names)
	}
	_, ffprobe, _ := testfixtures.FFmpeg()
	src := testfixtures.Path(t, "Movies/HDR Test (2021)/HDR Test (2021).mkv")
	for _, tm := range tms {
		t.Run(tm.Name, func(t *testing.T) {
			args := Args(Options{Path: src, Start: 6 * time.Second, Audio: -1, Encoder: enc, MaxHeight: 1080, Segment: 6 * time.Second, ToneMap: &tm})
			out := filepath.Join(t.TempDir(), "sdr.mp4")
			if b, err := exec.Command(ffmpeg, append(args[:len(args)-1], out)...).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, b)
			}
			got, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-count_frames",
				"-show_entries", "stream=codec_name,pix_fmt,color_transfer,color_primaries,nb_read_frames", "-of", "csv=p=0", out).Output()
			if err != nil {
				t.Fatal(err)
			}
			// 6 s of the fixture at 24 fps, as 8-bit SDR H.264.
			if s := strings.TrimRight(strings.TrimSpace(string(got)), ","); s != "h264,yuv420p,bt709,bt709,144" {
				t.Errorf("output: %s", s)
			}
		})
	}
}

func TestArgsToneMap(t *testing.T) {
	x264, _ := ByName("libx264")
	opencl, ok := ToneMapperByName("opencl")
	if !ok {
		t.Fatal("opencl unknown")
	}
	args := strings.Join(Args(Options{Path: "a.mkv", Audio: -1, Encoder: x264, MaxHeight: 1080, Segment: 6 * time.Second, ToneMap: &opencl}), " ")
	for _, want := range []string{
		"-init_hw_device opencl=tm -filter_hw_device tm -copyts",
		"-vf scale=-2:'min(ih,1080)',format=p010le,hwupload,tonemap_opencl=",
		"hwdownload,format=nv12,format=yuv420p -c:v libx264",
		"-color_primaries bt709 -color_trc bt709 -colorspace bt709",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("without %q:\n%s", want, args)
		}
	}
	if _, ok := ToneMapperByName("unknown"); ok {
		t.Error("unknown conversion accepted")
	}
}
