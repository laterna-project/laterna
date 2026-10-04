package fingerprint

import (
	"math/bits"
	"time"
)

const (
	// maxBits: two frames match if their codes differ by at most maxBits bits out of 32. Measured:
	// the same audio encoded twice differs by 0 to 5 bits (0 or 1 most of the time), while two
	// different pieces of music differ by 11 on average, and by 5 or less for 8% of frames.
	maxBits = 5
	// maxGap is how many frames in a row may fail to match in the middle of a shared range (a cut,
	// a sound over the intro): 0.75 s.
	maxGap = 6
	// minDensity is the share of frames of a shared range that must match, so that isolated hits
	// between two different pieces of music do not chain up.
	minDensity = 0.75
)

// Range is a time range in a file.
type Range struct{ Start, End time.Duration }

// Duration is the length of the range.
func (r Range) Duration() time.Duration { return r.End - r.Start }

// Common looks for the longest range a and b share, at least minLength long, whatever its offset
// from one fingerprint to the other. It returns the range in each of the two files; ok is false if
// there is none.
//
// Every possible offset is tried: for 10 minutes of audio on each side (4,800 frames) that is some
// twenty million comparisons and a few tens of milliseconds.
func Common(a, b Print, minLength time.Duration) (ra, rb Range, ok bool) {
	// The longest range wins. At equal length (an offset of one frame matches almost as well), the
	// one with the most matching frames does.
	bestLen, bestMatched, bestA, bestShift := 0, 0, 0, 0
	for shift := -(len(a.Codes) - 1); shift < len(b.Codes); shift++ {
		first := max(0, -shift)
		last := min(len(a.Codes), len(b.Codes)-shift) - 1
		runStart, lastMatch, matched := -1, -1, 0
		closeRun := func() {
			n := lastMatch - runStart + 1
			if runStart >= 0 && (n > bestLen || n == bestLen && matched > bestMatched) && float64(matched) >= minDensity*float64(n) {
				bestLen, bestMatched, bestA, bestShift = n, matched, runStart, shift
			}
			runStart, lastMatch, matched = -1, -1, 0
		}
		for i := first; i <= last; i++ {
			j := i + shift
			if a.Silent[i] || b.Silent[j] || bits.OnesCount32(a.Codes[i]^b.Codes[j]) > maxBits {
				if runStart >= 0 && i-lastMatch > maxGap {
					closeRun()
				}
				continue
			}
			if runStart < 0 {
				runStart = i
			}
			lastMatch = i
			matched++
		}
		closeRun()
	}
	if bestLen == 0 {
		return Range{}, Range{}, false
	}
	ra = a.span(bestA, bestA+bestLen-1)
	rb = b.span(bestA+bestShift, bestA+bestShift+bestLen-1)
	// A range cut off at the edge of a fingerprint has the shorter of the two lengths.
	if d := min(ra.Duration(), rb.Duration()); d < minLength {
		return Range{}, Range{}, false
	}
	return ra, rb, true
}

// span returns the range covered by frames first to last: from the middle of the first to the
// middle of the last, widened by half a hop on each side, and up to the edge of the fingerprint for
// a frame that touches it.
func (p Print) span(first, last int) Range {
	center := func(i int) time.Duration {
		return time.Duration(i*hop+frameSize/2) * time.Second / SampleRate
	}
	start, end := center(first)-Hop/2, center(last)+Hop/2
	if first == 0 {
		start = 0
	}
	if last == len(p.Codes)-1 {
		end = p.Length
	}
	return Range{Start: p.Start + max(0, start), End: p.Start + min(p.Length, end)}
}
