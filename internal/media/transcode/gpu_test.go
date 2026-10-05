package transcode

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

// Where the chain on the card works (there is no card in CI, so nothing to try there), it converts
// the HDR10 fixture like the CPU chain does: 8-bit BT.709 H.264 with keyframes on segment
// boundaries. 10-bit H.264, which no card decodes, goes through too (decoded on the CPU, processed
// on the card).
func TestGPU(t *testing.T) {
	ffmpeg, caps := detect(t)
	enc, _ := caps.Best()
	if !DetectGPU(context.Background(), ffmpeg, enc) {
		t.Skip("no Vulkan chain here")
	}
	_, ffprobe, _ := testfixtures.FFmpeg()
	src := testfixtures.Path(t, "Movies/HDR Test (2021)/HDR Test (2021).mkv")
	tm, _ := ToneMapperByName("zscale") // ignored: libplacebo converts on the card
	args := Args(Options{Path: src, Start: 6 * time.Second, Audio: -1, Encoder: enc, MaxHeight: 1080, Segment: 6 * time.Second, ToneMap: &tm, GPU: true})
	out := filepath.Join(t.TempDir(), "gpu.mp4")
	if b, err := exec.Command(ffmpeg, append(args[:len(args)-1], out)...).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	got, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-count_frames",
		"-show_entries", "stream=codec_name,pix_fmt,color_transfer,nb_read_frames", "-of", "csv=p=0", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.TrimRight(strings.TrimSpace(string(got)), ","); s != "h264,yuv420p,bt709,144" {
		t.Errorf("output: %s", s)
	}
	hi10p := filepath.Join(t.TempDir(), "hi10p.mkv")
	if b, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=2", "-c:v", "libx264",
		"-pix_fmt", "yuv420p10le", "file:"+hi10p).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	if starts := fragments(t, ffmpeg, Options{Path: hi10p, Audio: -1, Encoder: enc, MaxHeight: 1080, Segment: time.Second, GPU: true}); len(starts) != 2 {
		t.Errorf("10-bit H.264: %v", starts)
	}
	if starts := fragments(t, ffmpeg, Options{
		Path:  testfixtures.Path(t, "Movies/Dual Audio (2019)/Dual Audio (2019).mkv"),
		Audio: -1, Encoder: enc, MaxHeight: 1080, Segment: 6 * time.Second, GPU: true,
	}); !slices.Contains(starts, 6*time.Second) {
		t.Errorf("keyframe at 6 s missing: %v", starts)
	}
}

func TestArgsGPU(t *testing.T) {
	x264, _ := ByName("libx264")
	o := Options{Path: "a.mkv", Audio: -1, Encoder: x264, MaxHeight: 1080, Segment: 6 * time.Second, GPU: true}
	args := strings.Join(Args(o), " ")
	for _, want := range []string{
		"-hwaccel vulkan -hwaccel_device gpu -hwaccel_output_format vulkan -copyts",
		"-vf libplacebo=w=-2:h='min(ih,1080)':format=nv12,hwdownload,format=nv12,format=yuv420p -c:v libx264",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("without %q:\n%s", want, args)
		}
	}
	tm, _ := ToneMapperByName("opencl")
	o.ToneMap = &tm
	if a := strings.Join(Args(o), " "); !strings.Contains(a, ":tonemapping=bt.2390:colorspace=bt709") || strings.Contains(a, "opencl") {
		t.Errorf("HDR on the card: %s", a)
	}
	// Burn-in: CPU chain (overlay in memory).
	o.Burn = &Burn{File: "s.sup"}
	if a := strings.Join(Args(o), " "); strings.Contains(a, "hwaccel") || strings.Contains(a, "libplacebo") {
		t.Errorf("burn-in on the card: %s", a)
	}
}
