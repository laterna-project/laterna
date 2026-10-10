package domain

import (
	"slices"
	"time"
)

// Subtitle is a subtitle of a file: one of its streams, or an external file next to it. Subtitles
// are extracted at import time so they show up instantly.
type Subtitle struct {
	// Position in the file's subtitle list (streams first, then external files).
	Position int
	// StreamIndex is the stream number in the file; -1 for an external file.
	StreamIndex int
	// Path of the external file (empty for a stream).
	Path  string
	Codec string
	// Language is an ISO 639-2 code ("fre", "jpn"), empty if unknown.
	Language                         string
	Title                            string
	Default, Forced, HearingImpaired bool
	// Formats extracted and served ("ass", "vtt", "sup", "mks"). Empty if extraction failed.
	Formats []string
	// Width and Height of the canvas of an image subtitle (0 if unknown).
	Width, Height int
}

// External reports an external subtitle file.
func (s Subtitle) External() bool { return s.StreamIndex < 0 }

// Image reports an image subtitle (PGS, VobSub). A client that cannot draw it gets it burned into
// the picture.
func (s Subtitle) Image() bool {
	return slices.Contains(s.Formats, "sup") || slices.Contains(s.Formats, "mks")
}

// Font is a font attached to a file (Matroska attachment) for its ASS subtitles. Fonts are stored
// by hash, so one shared by every episode of a series is kept once.
type Font struct {
	// SHA256 of the content, in hex.
	SHA256 string
	// Names a subtitle may call it by, in lower case.
	Names []string
	// Ext is the file extension (".ttf", ".otf").
	Ext  string
	Size int64
}

// SubtitleSet is the result of extracting the subtitles of a file.
type SubtitleSet struct {
	// Fingerprint of the file when it was extracted.
	Fingerprint string
	// Sidecars is the signature of the external files (names, sizes, dates) at extraction time.
	// Adding, changing or removing one changes it.
	Sidecars    string
	ExtractedAt time.Time
	Subtitles   []Subtitle
	Fonts       []Font
}

// SubtitleWanted describes a subtitle a profile asks for.
type SubtitleWanted struct {
	// Language is a two-letter code ("fr"), one of those the search offers.
	Language        string
	HearingImpaired bool
	Forced          bool
}

// SubtitleSearchState says where a subtitle search stands.
type SubtitleSearchState string

// States of a subtitle search.
const (
	SubtitleSearching    SubtitleSearchState = "searching"
	SubtitleFound        SubtitleSearchState = "found"
	SubtitleNotFound     SubtitleSearchState = "not_found"
	SubtitleSearchFailed SubtitleSearchState = "failed"
)

// SubtitleSearch is a search for a subtitle of a file, handed to Bazarr.
type SubtitleSearch struct {
	FileID ID
	SubtitleWanted
	State SubtitleSearchState
	// Error says why a failed search could not go through.
	Error     *Text
	StartedAt time.Time
	// ProfileID is the profile that asked.
	ProfileID ID
}

// SubtitleLanguage is a language subtitles can be looked up in.
type SubtitleLanguage struct {
	// Code is its two-letter code.
	Code string
	Name string
}
