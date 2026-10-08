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

// Where a hardware decoder works (there is none in CI, so nothing to try there), the HDR10 fixture
// decoded on the card and converted on the CPU comes out like with the CPU chain: 8-bit BT.709 H.264
// with keyframes on segment boundaries. 10-bit H.264, which NVDEC cannot decode, falls back to the
// CPU instead of failing.
func TestDecoder(t *testing.T) {
	ffmpeg, caps := detect(t)
	enc, _ := caps.Best()
	d := DetectDecoder(context.Background(), ffmpeg, enc)
	if d == nil {
		t.Skip("no hardware decoder here")
	}
	_, ffprobe, _ := testfixtures.FFmpeg()
	src := testfixtures.Path(t, "Movies/HDR Test (2021)/HDR Test (2021).mkv")
	tm, _ := ToneMapperByName("zscale")
	args := Args(Options{Path: src, Start: 6 * time.Second, Audio: -1, Encoder: enc, MaxHeight: 1080, Segment: 6 * time.Second, ToneMap: &tm, Decoder: d})
	out := filepath.Join(t.TempDir(), "decoded.mp4")
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
	if starts := fragments(t, ffmpeg, Options{Path: hi10p, Audio: -1, Encoder: enc, MaxHeight: 1080, Segment: time.Second, Decoder: d}); len(starts) != 2 {
		t.Errorf("10-bit H.264: %v", starts)
	}
	if starts := fragments(t, ffmpeg, Options{
		Path:  testfixtures.Path(t, "Movies/Dual Audio (2019)/Dual Audio (2019).mkv"),
		Audio: -1, Encoder: enc, MaxHeight: 1080, Segment: 6 * time.Second, Decoder: d,
	}); !slices.Contains(starts, 6*time.Second) {
		t.Errorf("keyframe at 6 s missing: %v", starts)
	}
}

func TestArgsDecoder(t *testing.T) {
	x264, _ := ByName("libx264")
	cuda, ok := DecoderByName("cuda")
	if !ok {
		t.Fatal("no cuda decoder")
	}
	o := Options{Path: "a.mkv", Audio: -1, Encoder: x264, MaxHeight: 1080, Segment: 6 * time.Second, Decoder: &cuda}
	// Frames come back to memory: the CPU filters and the fallback of streams the card cannot decode.
	if a := strings.Join(Args(o), " "); !strings.Contains(a, "-hwaccel cuda -copyts -ss 0.000000 -i file:a.mkv") ||
		strings.Contains(a, "hwaccel_output_format") {
		t.Errorf("decoder: %s", a)
	}
	burn := o
	burn.Burn = &Burn{File: "s.sup"}
	if a := strings.Join(Args(burn), " "); !strings.Contains(a, "-hwaccel cuda") {
		t.Errorf("burn-in without the decoder: %s", a)
	}
	gpu := o
	gpu.GPU = true
	if a := strings.Join(Args(gpu), " "); strings.Contains(a, "cuda") {
		t.Errorf("decoder with the chain on the card: %s", a)
	}
	copied := o
	copied.CopyVideo = true
	if a := strings.Join(Args(copied), " "); strings.Contains(a, "hwaccel") {
		t.Errorf("decoder for a copied picture: %s", a)
	}
	file := strings.Join(FileArgs(FileOptions{Path: "a.mkv", Output: "b.mp4", Audio: -1, Encoder: x264, MaxHeight: 1080, Decoder: &cuda}), " ")
	if !strings.Contains(file, "-hwaccel cuda -i file:a.mkv") {
		t.Errorf("download: %s", file)
	}
}
