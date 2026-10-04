package transcode

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

// Whole files for downloads: MP4 without re-encoding, then scaled down and re-encoded. Progress is
// written to standard output.
func TestFileArgs(t *testing.T) {
	root := testfixtures.Library(t)
	ffmpeg, ffprobe, _ := testfixtures.FFmpeg()
	x264, _ := ByName("libx264")
	source := filepath.Join(root, "Films", "Deux Pistes (2019)", "Deux Pistes (2019).mkv")
	streams := func(path string) string {
		t.Helper()
		b, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,height,bit_rate",
			"-show_entries", "format=format_name", "-of", "csv=p=0", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}

	remux := filepath.Join(t.TempDir(), "remux.mp4")
	out, err := exec.Command(ffmpeg, FileArgs(FileOptions{Path: source, Output: remux, Audio: 2, CopyVideo: true, CopyAudio: true})...).Output()
	if err != nil || !strings.Contains(string(out), "progress=end") {
		t.Fatalf("remux: %v %s", err, out)
	}
	// Video and second audio stream copied, no subtitles or chapters.
	if got := streams(remux); !strings.HasPrefix(got, "h264,360,") || strings.Count(got, "aac") != 1 || !strings.Contains(got, "mp4") {
		t.Errorf("remux: %s", got)
	}

	small := filepath.Join(t.TempDir(), "petit.mp4")
	if b, err := exec.Command(ffmpeg, FileArgs(FileOptions{
		Path: source, Output: small, Audio: 1, Encoder: x264, MaxHeight: 240, VideoRate: 400, Channels: 2, AudioRate: 96,
	})...).CombinedOutput(); err != nil {
		t.Fatalf("transcode: %v %s", err, b)
	}
	if got := streams(small); !strings.HasPrefix(got, "h264,240,") || !strings.Contains(got, "aac") {
		t.Errorf("transcode: %s", got)
	}

	args := strings.Join(FileArgs(FileOptions{Path: source, Output: small, Audio: -1, Encoder: x264, MaxHeight: 720, VideoRate: 4000}), " ")
	if !strings.Contains(args, "-maxrate 4000k -bufsize 8000k") || strings.Contains(args, "-bf 0") || strings.Contains(args, "-c:a") {
		t.Errorf("arguments: %s", args)
	}
	vaapi, _ := ByName("h264_vaapi")
	if args := strings.Join(FileArgs(FileOptions{Path: source, Output: small, Audio: -1, Encoder: vaapi, VideoRate: 4000}), " "); strings.Contains(args, "-maxrate") {
		t.Errorf("bitrate cap for an encoder that does not support it: %s", args)
	}
}
