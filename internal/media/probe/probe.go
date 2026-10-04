// Package probe analyzes media files with ffprobe: container, duration, streams, chapters.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/proc"
)

// Prober runs ffprobe.
type Prober struct {
	bin     string
	timeout time.Duration
}

// New returns a Prober that uses the given executable ("" for ffprobe from PATH).
func New(bin string) *Prober {
	if bin == "" {
		bin = "ffprobe"
	}
	return &Prober{bin: bin, timeout: 2 * time.Minute}
}

// Check makes sure ffprobe starts: without it no file can be analyzed.
func (p *Prober) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := proc.Command(ctx, p.bin, "-version").CombinedOutput(); err != nil {
		return fmt.Errorf("ffprobe (%s) does not start: %w: %s", p.bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Probe analyzes a file. The path is passed with the "file:" prefix so that a file name cannot be
// taken for another protocol (http:, concat:...).
func (p *Prober) Probe(ctx context.Context, path string) (domain.MediaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	cmd := proc.Command(ctx, p.bin, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", "-show_chapters", "-i", "file:"+path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return domain.MediaInfo{}, fmt.Errorf("ffprobe %s: %w: %s", filepath.Base(path), err, msg)
	}
	return Parse(stdout.Bytes(), path)
}

// Parse reads ffprobe's JSON output. path helps narrow down the container from the extension.
func Parse(data []byte, path string) (domain.MediaInfo, error) {
	var out output
	if err := json.Unmarshal(data, &out); err != nil {
		return domain.MediaInfo{}, fmt.Errorf("unreadable ffprobe output: %w", err)
	}
	if out.Format.FormatName == "" {
		return domain.MediaInfo{}, errors.New("ffprobe did not recognise the format")
	}
	info := domain.MediaInfo{
		Container: container(out.Format.FormatName, path),
		Duration:  seconds(out.Format.Duration),
		Bitrate:   atoi64(out.Format.BitRate),
		Tags:      lowerKeys(out.Format.Tags),
	}
	for _, s := range out.Streams {
		info.Streams = append(info.Streams, stream(s))
	}
	for _, c := range out.Chapters {
		info.Chapters = append(info.Chapters, domain.Chapter{
			Start: seconds(c.StartTime), End: seconds(c.EndTime), Title: text(c.Tags["title"]),
		})
	}
	return info, nil
}

type output struct {
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
	Streams  []rawStream `json:"streams"`
	Chapters []struct {
		StartTime string            `json:"start_time"`
		EndTime   string            `json:"end_time"`
		Tags      map[string]string `json:"tags"`
	} `json:"chapters"`
}

type rawStream struct {
	Index            int               `json:"index"`
	CodecType        string            `json:"codec_type"`
	CodecName        string            `json:"codec_name"`
	Profile          string            `json:"profile"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	PixFmt           string            `json:"pix_fmt"`
	ColorTransfer    string            `json:"color_transfer"`
	BitsPerRawSample string            `json:"bits_per_raw_sample"`
	AvgFrameRate     string            `json:"avg_frame_rate"`
	RFrameRate       string            `json:"r_frame_rate"`
	Channels         int               `json:"channels"`
	ChannelLayout    string            `json:"channel_layout"`
	SampleRate       string            `json:"sample_rate"`
	BitRate          string            `json:"bit_rate"`
	Disposition      map[string]int    `json:"disposition"`
	Tags             map[string]string `json:"tags"`
	SideDataList     []struct {
		SideDataType string `json:"side_data_type"`
	} `json:"side_data_list"`
}

func stream(s rawStream) domain.Stream {
	tags := lowerKeys(s.Tags)
	st := domain.Stream{
		Index:           s.Index,
		Codec:           strings.ToLower(s.CodecName),
		Profile:         s.Profile,
		Language:        strings.ToLower(tags["language"]),
		Title:           tags["title"],
		Default:         s.Disposition["default"] == 1,
		Forced:          s.Disposition["forced"] == 1,
		HearingImpaired: s.Disposition["hearing_impaired"] == 1,
		Bitrate:         atoi64(s.BitRate),
	}
	if st.Language == "und" {
		st.Language = ""
	}
	switch s.CodecType {
	case "video":
		if s.Disposition["attached_pic"] == 1 {
			st.Kind = domain.StreamAttachment
			return st
		}
		st.Kind = domain.StreamVideo
		st.Width, st.Height, st.PixelFormat = s.Width, s.Height, s.PixFmt
		st.BitDepth = bitDepth(s.BitsPerRawSample, s.PixFmt)
		st.FrameRate = rate(s.AvgFrameRate)
		if st.FrameRate == 0 {
			st.FrameRate = rate(s.RFrameRate)
		}
		st.DynamicRange = dynamicRange(s)
	case "audio":
		st.Kind = domain.StreamAudio
		st.Channels, st.ChannelLayout = s.Channels, s.ChannelLayout
		st.SampleRate = int(atoi64(s.SampleRate))
	case "subtitle":
		st.Kind = domain.StreamSubtitle
		// Canvas size of an image subtitle (PGS, VobSub), needed to burn it in.
		st.Width, st.Height = s.Width, s.Height
	case "attachment":
		st.Kind = domain.StreamAttachment
	default:
		st.Kind = domain.StreamData
	}
	return st
}

func dynamicRange(s rawStream) domain.DynamicRange {
	for _, sd := range s.SideDataList {
		if strings.Contains(strings.ToLower(sd.SideDataType), "dovi") {
			return domain.DolbyVision
		}
	}
	switch s.ColorTransfer {
	case "smpte2084":
		return domain.HDR10
	case "arib-std-b67":
		return domain.HLG
	default:
		return domain.SDR
	}
}

// container names the file format, using the extension to pick within ffprobe's format families.
func container(formatName, path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch {
	case strings.Contains(formatName, "matroska"):
		if ext == "webm" {
			return "webm"
		}
		return "mkv"
	case strings.HasPrefix(formatName, "mov,mp4"):
		if ext == "mov" || ext == "m4a" || ext == "m4v" {
			return ext
		}
		return "mp4"
	case formatName == "mpegts":
		return "ts"
	default:
		first, _, _ := strings.Cut(formatName, ",")
		return first
	}
}

func bitDepth(raw, pixFmt string) int {
	if n := int(atoi64(raw)); n > 0 {
		return n
	}
	switch {
	case strings.Contains(pixFmt, "12"):
		return 12
	case strings.Contains(pixFmt, "10"):
		return 10
	case pixFmt == "":
		return 0
	default:
		return 8
	}
}

func rate(r string) float64 {
	num, den, ok := strings.Cut(r, "/")
	if !ok {
		f, _ := strconv.ParseFloat(r, 64)
		return f
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

func seconds(s string) time.Duration {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0
	}
	return time.Duration(f * float64(time.Second)).Round(time.Millisecond)
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// text makes a string safe: file metadata may hold invalid UTF-8, which encoding/json (v2) would
// reject later on.
func text(s string) string { return strings.TrimSpace(strings.ToValidUTF8(s, "�")) }

func lowerKeys(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = text(v)
	}
	return out
}
