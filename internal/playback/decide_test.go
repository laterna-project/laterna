package playback

import (
	"fmt"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
)

// Typical device profiles. Test examples, not something the server assumes.
var (
	chrome = DeviceProfile{
		Containers: []string{"mp4", "webm"}, Video: []VideoSupport{{Codec: "h264"}, {Codec: "vp9", MaxBitDepth: 10}, {Codec: "av1", MaxBitDepth: 10}},
		AudioCodecs: []string{"aac", "mp3", "opus", "flac"}, HLS: true,
	}
	tv = DeviceProfile{
		Containers: []string{"mp4", "mkv"}, Video: []VideoSupport{{Codec: "h264"}, {Codec: "hevc", MaxBitDepth: 10, HDR: true}},
		AudioCodecs: []string{"aac", "ac3", "eac3"}, HLS: true,
	}
)

func file(container string, streams ...domain.Stream) domain.MediaInfo {
	for i := range streams {
		streams[i].Index = i
	}
	return domain.MediaInfo{Container: container, Streams: streams}
}

var (
	h264     = domain.Stream{Kind: domain.StreamVideo, Codec: "h264", BitDepth: 8, DynamicRange: domain.SDR}
	hevc10   = domain.Stream{Kind: domain.StreamVideo, Codec: "hevc", BitDepth: 10, DynamicRange: domain.SDR}
	hevcHDR  = domain.Stream{Kind: domain.StreamVideo, Codec: "hevc", BitDepth: 10, DynamicRange: domain.HDR10}
	aac      = domain.Stream{Kind: domain.StreamAudio, Codec: "aac"}
	flacJpn  = domain.Stream{Kind: domain.StreamAudio, Codec: "flac", Language: "jpn", Default: true}
	eac3     = domain.Stream{Kind: domain.StreamAudio, Codec: "eac3"}
	assTrack = domain.Stream{Kind: domain.StreamSubtitle, Codec: "ass"}
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name   string
		info   domain.MediaInfo
		dev    DeviceProfile
		audio  int
		method Method
		track  int
	}{
		{"MP4 H.264/AAC in Chrome", file("mp4", h264, aac), chrome, -1, Direct, 1},
		{"MKV H.264/AAC in Chrome: only the container is a problem", file("mkv", h264, aac, assTrack), chrome, -1, Remux, 1},
		{"MKV 10-bit HEVC in Chrome: video re-encoded", file("mkv", hevc10, flacJpn), chrome, -1, Transcode, 1},
		{"MKV 10-bit HEVC on a TV that plays MKV", file("mkv", hevc10, aac), tv, -1, Direct, 1},
		{"MKV HEVC HDR, E-AC-3, on the TV", file("mkv", hevcHDR, eac3), tv, -1, Direct, 1},
		{"HDR on a device without HDR: transcode", file("mkv", hevcHDR, aac), DeviceProfile{Containers: []string{"mkv"}, Video: []VideoSupport{{Codec: "hevc", MaxBitDepth: 10}, {Codec: "h264"}}, AudioCodecs: []string{"aac"}, HLS: true}, -1, Transcode, 1},
		{"E-AC-3 in Chrome: transcode (audio)", file("mkv", h264, eac3), chrome, -1, Transcode, 1},
		{"default stream preferred", file("mkv", h264, aac, flacJpn), chrome, -1, Remux, 2},
		{"requested stream", file("mkv", h264, aac, flacJpn), chrome, 1, Remux, 1},
		{"no HLS and no direct play", file("mkv", h264, aac), DeviceProfile{Video: []VideoSupport{{Codec: "h264"}}, AudioCodecs: []string{"aac"}}, -1, Unplayable, 1},
		{"no H.264: nothing to transcode to", file("mkv", hevc10, aac), DeviceProfile{Video: []VideoSupport{{Codec: "vp9"}}, AudioCodecs: []string{"aac"}, HLS: true}, -1, Unplayable, 1},
	}
	for _, tt := range tests {
		p := Decide(tt.info, tt.dev, tt.audio, nil)
		if p.Method != tt.method || p.Audio != tt.track || p.Video != 0 {
			t.Errorf("%s: %+v, want %s with audio stream %d", tt.name, p, tt.method, tt.track)
		}
		if p.Method != Direct && len(p.Reasons) == 0 {
			t.Errorf("%s: no reason given", tt.name)
		}
	}
	p := Decide(file("mkv", hevc10, eac3), chrome, -1, nil)
	if got := fmt.Sprint(p.Reasons); got != "[reason.video_unsupported (bit_depth=10, codec=hevc, dynamic_range=sdr) reason.audio_unsupported (codec=eac3) reason.container_unsupported (container=mkv)]" {
		t.Errorf("reasons: %v", p.Reasons)
	}
}

// Each stream gets its own decision: only what has to be re-encoded is.
func TestDecideCopiesWhatItCan(t *testing.T) {
	cases := []struct {
		name                 string
		info                 domain.MediaInfo
		dev                  DeviceProfile
		copyVideo, copyAudio bool
		toneMap              bool
	}{
		{"E-AC-3 in Chrome: only the audio is re-encoded", file("mkv", h264, eac3), chrome, true, false, false},
		{"10-bit HEVC in Chrome: only the video is re-encoded", file("mkv", hevc10, flacJpn), chrome, false, true, false},
		{"HDR on an SDR screen: tone mapped", file("mkv", hevcHDR, aac), chrome, false, true, true},
		{"HDR on the HDR TV: nothing to re-encode", file("mkv", hevcHDR, eac3), DeviceProfile{Video: tv.Video, AudioCodecs: tv.AudioCodecs, HLS: true}, true, true, false},
	}
	for _, c := range cases {
		p := Decide(c.info, c.dev, -1, nil)
		if p.CopyVideo != c.copyVideo || p.CopyAudio != c.copyAudio || p.ToneMap != c.toneMap {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
}

// Music: a file without video plays as is or converted to AAC, never over HLS. An attached cover
// picture does not count as video.
func TestDecideAudio(t *testing.T) {
	cover := domain.Stream{Kind: domain.StreamAttachment, Codec: "mjpeg"}
	flac := domain.Stream{Kind: domain.StreamAudio, Codec: "flac"}
	wma := domain.Stream{Kind: domain.StreamAudio, Codec: "wmav2"}
	web := DeviceProfile{Containers: []string{"mp4", "flac", "mp3"}, AudioCodecs: []string{"aac", "mp3", "flac"}}
	phone := DeviceProfile{Containers: []string{"m4a"}, AudioCodecs: []string{"aac"}}
	tests := []struct {
		name   string
		info   domain.MediaInfo
		dev    DeviceProfile
		method Method
	}{
		{"FLAC with a cover in a browser", file("flac", flac, cover), web, Direct},
		{"FLAC on a device that cannot play it: converted", file("flac", flac, cover), phone, Convert},
		{"WMA: converted", file("asf", wma), web, Convert},
		{"no AAC: nothing we can do", file("asf", wma), DeviceProfile{Containers: []string{"mp4"}, AudioCodecs: []string{"mp3"}}, Unplayable},
		{"neither picture nor sound", file("flac", cover), web, Unplayable},
	}
	for _, tt := range tests {
		p := Decide(tt.info, tt.dev, -1, nil)
		if p.Method != tt.method || p.Video != -1 || p.Burn || p.ToneMap {
			t.Errorf("%s: %+v, want %s", tt.name, p, tt.method)
		}
		if p.Method == Convert && (p.CopyAudio || p.Audio != 0 || len(p.Reasons) == 0) {
			t.Errorf("%s: conversion badly described: %+v", tt.name, p)
		}
	}
}

// A subtitle the device renders does not change playback. Otherwise it is burned in, which
// re-encodes the picture and never happens in direct play.
func TestDecideSubtitles(t *testing.T) {
	mp4 := file("mp4", h264, aac)
	ass := domain.Subtitle{Codec: "ass", Formats: []string{"ass", "vtt"}}
	pgs := domain.Subtitle{Codec: "hdmv_pgs_subtitle", Formats: []string{"sup"}}
	web := chrome
	web.SubtitleFormats = []string{"vtt"}
	withPGS := web
	withPGS.SubtitleFormats = []string{"vtt", "ass", "sup"}
	cases := []struct {
		name   string
		dev    DeviceProfile
		sub    *domain.Subtitle
		method Method
		burn   bool
	}{
		{"no subtitle", web, nil, Direct, false},
		{"ASS shown as WebVTT", web, &ass, Direct, false},
		{"PGS not rendered: burned in", web, &pgs, Transcode, true},
		{"PGS rendered by the device", withPGS, &pgs, Direct, false},
		{"no subtitle format at all: text burned in", chrome, &ass, Transcode, true},
	}
	for _, c := range cases {
		p := Decide(mp4, c.dev, -1, c.sub)
		if p.Method != c.method || p.Burn != c.burn || (c.burn && p.CopyVideo) {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
	// HDR plus burn-in: the re-encoded picture needs tone mapping.
	if p := Decide(file("mkv", hevcHDR, aac), DeviceProfile{Video: tv.Video, AudioCodecs: tv.AudioCodecs, HLS: true}, -1, &pgs); !p.Burn || !p.ToneMap {
		t.Errorf("HDR with burn-in: %+v", p)
	}
}
