package playback

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func secs(vs ...float64) []time.Duration {
	out := make([]time.Duration, len(vs))
	for i, v := range vs {
		out[i] = time.Duration(v * float64(time.Second))
	}
	return out
}

func TestSegments(t *testing.T) {
	// Irregular keyframes, as in a real episode (0.08 s to 10 s apart).
	keys := secs(0, 0.08, 2, 6.256, 9.176, 11.178, 13.68, 20.979, 23.941, 29.655)
	segs := Segments(keys, secs(31)[0], 6*time.Second)
	want := secs(0, 6.256, 13.68, 20.979, 29.655)
	if len(segs) != len(want) {
		t.Fatalf("%d segments: %v", len(segs), segs)
	}
	for i, s := range segs {
		if s.Start != want[i] {
			t.Errorf("segment %d starts at %v, want %v", i, s.Start, want[i])
		}
		if i > 0 && segs[i-1].End != s.Start {
			t.Errorf("gap between %d and %d", i-1, i)
		}
	}
	if segs[len(segs)-1].End != secs(31)[0] {
		t.Errorf("end: %v", segs[len(segs)-1].End)
	}
	// First keyframe after zero, keyframes past the duration: both handled.
	if segs := Segments(secs(0.5, 7, 40), secs(10)[0], 6*time.Second); len(segs) != 2 || segs[0].Start != 0 || segs[1].Start != secs(7)[0] {
		t.Errorf("edge cases: %v", segs)
	}
	if Segments(keys, 0, 6*time.Second) != nil {
		t.Error("zero duration")
	}
}

func TestFixedSegments(t *testing.T) {
	segs := FixedSegments(secs(16)[0], 6*time.Second)
	if len(segs) != 3 || segs[1].Start != 6*time.Second || segs[2].End != secs(16)[0] || segs[2].Start != 12*time.Second {
		t.Errorf("fixed segments: %v", segs)
	}
	// A remainder shorter than half a segment (audio slightly longer than video) goes into the last
	// one.
	if segs := FixedSegments(secs(12.021)[0], 6*time.Second); len(segs) != 2 || segs[1].End != secs(12.021)[0] {
		t.Errorf("short remainder: %v", segs)
	}
	if segs := FixedSegments(secs(2)[0], 6*time.Second); len(segs) != 1 || segs[0].End != secs(2)[0] {
		t.Errorf("short file: %v", segs)
	}
	if FixedSegments(0, 6*time.Second) != nil {
		t.Error("zero duration")
	}
}

func TestFind(t *testing.T) {
	segs := Segments(secs(0, 6, 12, 18), secs(24)[0], 6*time.Second)
	for at, want := range map[float64]int{0: 0, 5.9: 0, 6: 1, 17.99: 2, 18: 3, 100: 3} {
		if got := Find(segs, secs(at)[0]); got != want {
			t.Errorf("Find(%v) = %d, want %d", at, got, want)
		}
	}
}

func TestPlaylist(t *testing.T) {
	segs := Segments(secs(0, 6.256, 16.2), secs(20)[0], 6*time.Second)
	pl := Playlist(segs, "init.mp4", func(i int) string { return strconv.Itoa(i) + ".m4s" })
	for _, line := range []string{
		"#EXT-X-TARGETDURATION:10", "#EXT-X-PLAYLIST-TYPE:VOD", `#EXT-X-MAP:URI="init.mp4"`,
		"#EXTINF:6.256000,\n0.m4s", "#EXTINF:9.944000,\n1.m4s", "#EXTINF:3.800000,\n2.m4s", "#EXT-X-ENDLIST",
	} {
		if !strings.Contains(pl, line) {
			t.Errorf("playlist without %q:\n%s", line, pl)
		}
	}
}

func TestMasterPlaylist(t *testing.T) {
	pl := MasterPlaylist("main.m3u8", 4_000_000, []Rendition{
		{Name: `Signs "& songs"`, Language: "fr", URI: "subtitles/0.m3u8"},
		{Name: "English\nSDH", Language: "en", HearingImpaired: true, URI: "subtitles/3.m3u8"},
		{Name: "#5", URI: "subtitles/5.m3u8"},
	})
	want := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n" +
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Signs '& songs'",DEFAULT=NO,AUTOSELECT=YES,LANGUAGE="fr",URI="subtitles/0.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="EnglishSDH",DEFAULT=NO,AUTOSELECT=YES,LANGUAGE="en",` +
		`CHARACTERISTICS="public.accessibility.transcribes-spoken-dialog,public.accessibility.describes-music-and-sound",URI="subtitles/3.m3u8"` + "\n" +
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="#5",DEFAULT=NO,AUTOSELECT=YES,URI="subtitles/5.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=4000000,SUBTITLES="subs"` + "\nmain.m3u8\n"
	if pl != want {
		t.Errorf("master playlist:\n%s\nwant:\n%s", pl, want)
	}
	// Without subtitles, the variant names no group.
	if pl := MasterPlaylist("main.m3u8", 0, nil); !strings.HasSuffix(pl, "#EXT-X-STREAM-INF:BANDWIDTH=1\nmain.m3u8\n") {
		t.Errorf("master playlist without subtitles:\n%s", pl)
	}
}

func TestSubtitlePlaylist(t *testing.T) {
	pl := SubtitlePlaylist(1420500*time.Millisecond, "0.hls.vtt")
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:1421\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXTINF:1420.500000,\n0.hls.vtt\n#EXT-X-ENDLIST\n"
	if pl != want {
		t.Errorf("subtitle playlist:\n%s\nwant:\n%s", pl, want)
	}
}

func TestHLSWebVTT(t *testing.T) {
	const cue = "\n00:00:01.000 --> 00:00:02.000\nHello\n"
	for in, want := range map[string]string{
		"WEBVTT\n" + cue:                        "WEBVTT\n" + timestampMap + "\n" + cue,
		"WEBVTT\r\n\r\n1\r\n":                   "WEBVTT\r\n" + timestampMap + "\r\n\r\n1\r\n",
		"\ufeffWEBVTT - Kind: captions\n" + cue: "\ufeffWEBVTT - Kind: captions\n" + timestampMap + "\n" + cue,
		"WEBVTT":                                "WEBVTT\n" + timestampMap + "\n",
		"":                                      "WEBVTT\n" + timestampMap + "\n\n",
		"WEBVTT\n" + timestampMap + "\n" + cue:  "WEBVTT\n" + timestampMap + "\n" + cue,
	} {
		if got := string(HLSWebVTT([]byte(in))); got != want {
			t.Errorf("HLSWebVTT(%q) = %q, want %q", in, got, want)
		}
	}
}
