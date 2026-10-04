package domain

import "time"

// SegmentKind is the kind of a part a player may offer to skip.
type SegmentKind string

const (
	// SegmentIntro is the opening credits.
	SegmentIntro SegmentKind = "intro"
	// SegmentCredits is the end credits.
	SegmentCredits SegmentKind = "credits"
	// SegmentRecap is the "previously on" part.
	SegmentRecap SegmentKind = "recap"
	// SegmentPreview is the preview of the next episode.
	SegmentPreview SegmentKind = "preview"
)

// SegmentSource says where a segment comes from.
type SegmentSource string

const (
	// SegmentFromChapters is a named chapter of the file ("Opening", "Ending"...).
	SegmentFromChapters SegmentSource = "chapters"
	// SegmentFromAudio is the same audio found in another episode of the season.
	SegmentFromAudio SegmentSource = "audio"
)

// Segment is a part of a file. There is at most one of each kind per file, and a chapter wins over
// audio.
type Segment struct {
	Kind       SegmentKind
	Start, End time.Duration
	Source     SegmentSource
}
