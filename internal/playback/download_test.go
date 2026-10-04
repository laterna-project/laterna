package playback

import (
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
)

func TestDecideDownload(t *testing.T) {
	phone := DeviceProfile{Containers: []string{"mp4"}, Video: []VideoSupport{{Codec: "h264"}}, AudioCodecs: []string{"aac", "mp3"}}
	web := DeviceProfile{Containers: []string{"mp4", "flac", "mp3"}, AudioCodecs: []string{"aac", "mp3", "flac"}}
	sized := func(s domain.Stream, h int) domain.Stream { s.Height = h; return s }
	media := func(container string, kbps int64, streams ...domain.Stream) domain.MediaInfo {
		info := file(container, streams...)
		info.Duration, info.Bitrate = time.Hour, kbps*1000
		return info
	}
	flac := domain.Stream{Kind: domain.StreamAudio, Codec: "flac", Channels: 2}
	const size = 3 << 30
	tests := []struct {
		name                 string
		info                 domain.MediaInfo
		dev                  DeviceProfile
		q                    domain.DownloadQuality
		method               Method
		copyVideo, copyAudio bool
		maxHeight, audioRate int
		toneMap              bool
	}{
		{"MP4 lisible, original : tel quel", media("mp4", 5000, sized(h264, 1080), aac), phone, domain.DownloadOriginal, Direct, true, true, 0, 0, false},
		{"light playable MP4, high quality: as is", media("mp4", 5000, sized(h264, 1080), aac), phone, domain.DownloadHigh, Direct, true, true, 0, 0, false},
		{"1080p MP4 at medium quality: scaled down", media("mp4", 5000, sized(h264, 1080), aac), phone, domain.DownloadMedium, Transcode, false, false, 720, 160, false},
		{"playable 720p MKV: MP4 without re-encoding", media("mkv", 3000, sized(h264, 720), aac), phone, domain.DownloadHigh, Remux, true, true, 0, 0, false},
		{"HEVC HDR for a phone: H.264 SDR", media("mkv", 20000, sized(hevcHDR, 2160), flacJpn), phone, domain.DownloadOriginal, Transcode, false, false, 1080, 0, true},
		{"HEVC HDR on the TV, original: as is", media("mkv", 20000, sized(hevcHDR, 2160), eac3), tv, domain.DownloadOriginal, Direct, true, true, 0, 0, false},
		{"HEVC HDR on the TV, low: 480p SDR", media("mkv", 20000, sized(hevcHDR, 2160), eac3), tv, domain.DownloadLow, Transcode, false, false, 480, 128, true},
		{"no MP4: nothing to prepare", media("mkv", 20000, sized(hevc10, 1080), aac), DeviceProfile{Containers: []string{"webm"}, Video: []VideoSupport{{Codec: "vp9"}}, AudioCodecs: []string{"opus"}}, domain.DownloadHigh, Unplayable, false, false, 0, 0, false},
		{"FLAC in a browser, original: as is", media("flac", 1000, flac), web, domain.DownloadOriginal, Direct, false, true, 0, 0, false},
		{"FLAC, low quality: AAC", media("flac", 1000, flac), web, domain.DownloadLow, Convert, false, false, 0, 96, false},
		{"FLAC for a phone, original: same AAC as playback", media("flac", 1000, flac), phone, domain.DownloadOriginal, Convert, false, false, 0, 0, false},
		{"light MP3, medium quality: as is", media("mp3", 128, domain.Stream{Kind: domain.StreamAudio, Codec: "mp3"}), web, domain.DownloadMedium, Direct, false, true, 0, 0, false},
		{"MP3 on a device that only plays MP4: AAC", media("mp3", 128, domain.Stream{Kind: domain.StreamAudio, Codec: "mp3"}), phone, domain.DownloadMedium, Convert, false, false, 0, 160, false},
	}
	for _, tt := range tests {
		p := DecideDownload(tt.info, size, tt.dev, -1, tt.q)
		if p.Method != tt.method || p.CopyVideo != tt.copyVideo || p.CopyAudio != tt.copyAudio ||
			p.MaxHeight != tt.maxHeight || p.AudioRate != tt.audioRate || p.ToneMap != tt.toneMap {
			t.Errorf("%s:\n  %+v", tt.name, p)
		}
		switch {
		case p.Method == Unplayable:
		case p.Estimate <= 0:
			t.Errorf("%s: estimated size %d", tt.name, p.Estimate)
		case p.Method == Direct && p.Estimate != size:
			t.Errorf("%s: as is, the size is the file's: %d", tt.name, p.Estimate)
		}
		if p.Method != Direct && len(p.Reasons) == 0 {
			t.Errorf("%s: no reason given", tt.name)
		}
	}
	// Expected size of one hour at medium quality: (4000 + 160) kbit/s.
	p := DecideDownload(media("mp4", 5000, sized(h264, 1080), aac), size, phone, -1, domain.DownloadMedium)
	if want := int64(3600 * 4160 * 1000 / 8); p.Estimate != want || p.VideoRate != 4000 {
		t.Errorf("estimated size: %d, want %d (%+v)", p.Estimate, want, p)
	}
}
