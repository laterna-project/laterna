package probe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/testfixtures"
)

func sample(t *testing.T, name, path string) domain.MediaInfo {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := Parse(raw, path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestParseMultiTrackMKV(t *testing.T) {
	info := sample(t, "multitrack", "x.mkv")
	if info.Container != "mkv" || info.Duration != 12021*time.Millisecond || info.Bitrate == 0 {
		t.Errorf("format: %+v", info)
	}
	if len(info.Streams) != 5 {
		t.Fatalf("%d streams", len(info.Streams))
	}
	v, fr, ja, ass, srt := info.Streams[0], info.Streams[1], info.Streams[2], info.Streams[3], info.Streams[4]
	if v.Kind != domain.StreamVideo || v.Codec != "h264" || v.Width != 640 || v.BitDepth != 8 || v.FrameRate != 24 || v.DynamicRange != domain.SDR {
		t.Errorf("video: %+v", v)
	}
	if fr.Kind != domain.StreamAudio || fr.Language != "fre" || fr.Title != "French" || !fr.Default || fr.Channels != 2 || fr.SampleRate != 48000 {
		t.Errorf("French audio: %+v", fr)
	}
	if ja.Language != "jpn" || ja.Default || ja.Title != "日本語" {
		t.Errorf("Japanese audio: %+v", ja)
	}
	if ass.Kind != domain.StreamSubtitle || ass.Codec != "ass" || srt.Codec != "subrip" || srt.Language != "eng" {
		t.Errorf("subtitles: %+v / %+v", ass, srt)
	}
	if len(info.Chapters) != 3 || info.Chapters[0].Title != "OP" || info.Chapters[2].Start != 9*time.Second || info.Chapters[2].End != 12*time.Second {
		t.Errorf("chapters: %+v", info.Chapters)
	}
}

func TestParseHDR(t *testing.T) {
	info := sample(t, "hdr10", "x.mkv")
	v, a := info.Streams[0], info.Streams[1]
	if v.Codec != "hevc" || v.Profile != "Main 10" || v.BitDepth != 10 || v.DynamicRange != domain.HDR10 {
		t.Errorf("HDR video: %+v", v)
	}
	if a.Codec != "eac3" || a.Channels != 6 || a.ChannelLayout != "5.1(side)" && a.ChannelLayout != "5.1" {
		t.Errorf("5.1 audio: %+v", a)
	}
}

func TestParseMP3WithCover(t *testing.T) {
	info := sample(t, "mp3", "track.mp3")
	if info.Container != "mp3" || info.Tags["title"] != "Track Two" || info.Tags["album"] != "Album Test" {
		t.Errorf("MP3: %+v", info)
	}
	if len(info.Streams) != 2 || info.Streams[1].Kind != domain.StreamAttachment {
		t.Errorf("the cover must be an attachment, not a video: %+v", info.Streams)
	}
}

func TestContainerNames(t *testing.T) {
	tests := []struct{ format, path, want string }{
		{"matroska,webm", "a.mkv", "mkv"},
		{"matroska,webm", "a.WEBM", "webm"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "a.mp4", "mp4"},
		{"mov,mp4,m4a,3gp,3g2,mj2", "a.mov", "mov"},
		{"mpegts", "a.m2ts", "ts"},
		{"avi", "a.avi", "avi"},
	}
	for _, tt := range tests {
		if got := container(tt.format, tt.path); got != tt.want {
			t.Errorf("container(%q, %q) = %q", tt.format, tt.path, got)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "{", `{"format":{}}`} {
		if _, err := Parse([]byte(in), "x"); err == nil {
			t.Errorf("Parse(%q) accepted", in)
		}
	}
}

func TestProbeRealFiles(t *testing.T) {
	_, ffprobe, _ := testfixtures.FFmpeg()
	p := New(ffprobe)
	ctx := context.Background()
	info, err := p.Probe(ctx, testfixtures.Path(t, "Movies/Dual Audio (2019)/Dual Audio (2019).mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Container != "mkv" || len(info.Streams) != 5 || len(info.Chapters) != 3 {
		t.Fatalf("real probe: %+v", info)
	}
	if info.Streams[2].Title != "日本語" || info.Chapters[1].Title != "Episode" {
		t.Errorf("non-UTF-8 text (titles): %q, %q", info.Streams[2].Title, info.Chapters[1].Title)
	}
	// A name that looks like a protocol is still a file name (the "file:" prefix).
	if _, err := p.Probe(ctx, filepath.Join(t.TempDir(), "http:example.com.mkv")); err == nil {
		t.Error("missing file: want an error")
	}
	if _, err := New(filepath.Join(t.TempDir(), "absent")).Probe(ctx, "x"); err == nil {
		t.Error("missing executable: want an error")
	}
}

func TestCheck(t *testing.T) {
	if err := New(filepath.Join(t.TempDir(), "absent")).Check(context.Background()); err == nil {
		t.Error("missing ffprobe not reported")
	}
	if _, ffprobe, ok := testfixtures.FFmpeg(); ok {
		if err := New(ffprobe).Check(context.Background()); err != nil {
			t.Error(err)
		}
	}
}
