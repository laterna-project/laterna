package domain

import "time"

// StreamKind is the kind of a stream in a file.
type StreamKind string

// Stream kinds.
const (
	StreamVideo    StreamKind = "video"
	StreamAudio    StreamKind = "audio"
	StreamSubtitle StreamKind = "subtitle"
	// StreamAttachment covers embedded fonts and attached pictures. Never played.
	StreamAttachment StreamKind = "attachment"
	StreamData       StreamKind = "data"
)

// DynamicRange is the dynamic range of a video stream.
type DynamicRange string

// Known dynamic ranges.
const (
	SDR         DynamicRange = "sdr"
	HDR10       DynamicRange = "hdr10"
	HLG         DynamicRange = "hlg"
	DolbyVision DynamicRange = "dolby_vision"
)

// Stream is one stream of a media file, as the probe saw it.
type Stream struct {
	// Index is the stream's position in the file, as numbered by the container.
	Index           int
	Kind            StreamKind
	Codec           string
	Profile         string
	Language        string // ISO 639-2 code as written in the file ("fre", "jpn"...)
	Title           string
	Default         bool
	Forced          bool
	HearingImpaired bool
	// Video.
	Width, Height int
	BitDepth      int
	FrameRate     float64
	DynamicRange  DynamicRange
	PixelFormat   string
	// Audio.
	Channels      int
	ChannelLayout string
	SampleRate    int
	Bitrate       int64
}

// Chapter is a chapter of a file.
type Chapter struct {
	Start, End time.Duration
	Title      string
}

// MediaInfo is what the probe found in a file.
type MediaInfo struct {
	// Container is the file format ("mkv", "mp4", "webm"...).
	Container string
	Duration  time.Duration
	Bitrate   int64
	Streams   []Stream
	Chapters  []Chapter
	// Tags are the file-level metadata (title, artist, album...), with lower-case keys.
	Tags map[string]string
}

// MediaFile is a file of a library.
type MediaFile struct {
	ID        ID
	LibraryID ID
	// Path is the absolute path on the server.
	Path        string
	Size        int64
	ModTime     time.Time
	Fingerprint string
	// MissingSince is when the file was first seen missing, nil while it is there.
	MissingSince *time.Time
	// AnalyzedAt is the last successful analysis, nil if there was none.
	AnalyzedAt *time.Time
	Info       MediaInfo
	// Segments are intros, credits and other skippable parts, ordered by start.
	Segments []Segment
}

// Trickplay describes the scrubbing thumbnails of a video file: Count thumbnails, one every
// Interval, Width x Height pixels each, laid out row by row on Sheets sheets of Columns x Rows.
type Trickplay struct {
	FileID ID
	// Fingerprint of the file when the thumbnails were generated.
	Fingerprint string
	// Key names the generation (folder of the sheets, URL). Empty for a file with no picture.
	Key           string
	Interval      time.Duration
	Width, Height int
	Columns, Rows int
	Count, Sheets int
	CreatedAt     time.Time
}
