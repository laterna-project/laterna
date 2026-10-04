// Package keyframes builds the keyframe index of the video stream of a file (presentation times).
// HLS playback is cut into segments from it. The Cues of a Matroska file give it after reading a
// few kilobytes; otherwise ffprobe reads the whole file.
package keyframes

import (
	"errors"
	"time"

	"github.com/laterna-project/laterna/internal/media/matroska"
)

// ErrNoIndex is returned for a file without a usable index (Matroska without Cues, another format).
// The index then has to be computed by reading the file.
var ErrNoIndex = matroska.ErrNoIndex

// Matroska reads the keyframes of the first video track from the Cues of a Matroska file (MKV,
// WebM). It returns ErrNoIndex if the file is not Matroska, has no Cues or has no video track.
func Matroska(path string) ([]time.Duration, error) {
	f, err := matroska.Open(path)
	if errors.Is(err, matroska.ErrNotMatroska) {
		return nil, ErrNoIndex
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.Keyframes()
}
