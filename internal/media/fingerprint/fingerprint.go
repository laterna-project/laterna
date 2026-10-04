// Package fingerprint computes the audio fingerprint of a stretch of audio and finds what two
// fingerprints have in common: the intro of one episode inside another, wherever it starts.
//
// FFmpeg decodes the audio to 16-bit mono at 11,025 Hz (Args). A frame of 4,096 samples (0.37 s) is
// taken every 1,365 samples (0.124 s). Each frame gives the energy of the 12 pitch classes (chroma:
// each note, all octaves together), smoothed over 5 frames, then a 32-bit code that describes the
// shape of that chroma. The same audio gives almost the same codes from one file to another, even
// when encoded differently, while different audio gives unrelated codes. Frames that are too quiet
// (silence) match nothing: two silences do not make an intro.
package fingerprint

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
)

const (
	// SampleRate is the rate of the decoded audio.
	SampleRate = 11025
	frameSize  = 4096
	hop        = 1365
	// Frequency band kept: below it the transform cannot tell notes apart, above it harmonics blur
	// the chroma.
	minFreq = 100
	maxFreq = 3520
	// silenceRMS is the level under which a frame counts as silence (about -50 dB).
	silenceRMS = 0.003
)

// Hop is the time between two frames.
const Hop = time.Duration(hop) * time.Second / SampleRate

// Print is the fingerprint of a stretch of audio: one code per frame.
type Print struct {
	// Start is where it begins in the file and Length how long it is.
	Start, Length time.Duration
	Codes         []uint32
	// Silent marks the frames that are too quiet to compare.
	Silent []bool
}

// Args returns the FFmpeg arguments that decode stream (its position in the file) from start to
// start + length as 16-bit little-endian mono at SampleRate, on standard output.
func Args(path string, stream int, start, length time.Duration) []string {
	return []string{
		"-nostdin", "-hide_banner", "-v", "error",
		"-ss", seconds(start), "-t", seconds(length), "-i", "file:" + path,
		"-map", "0:" + strconv.Itoa(stream), "-vn", "-sn", "-dn",
		"-ac", "1", "-ar", strconv.Itoa(SampleRate), "-c:a", "pcm_s16le", "-f", "s16le", "pipe:1",
	}
}

func seconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 3, 64) }

// Compute fingerprints the audio read from r (in the format of Args), which starts at start in the
// file. The audio is read as a stream, so memory use does not depend on its length.
func Compute(r io.Reader, start time.Duration) (Print, error) {
	var (
		br      = bufio.NewReaderSize(r, 64<<10)
		chunk   = make([]byte, 2*hop)
		pending = make([]float64, 0, frameSize+hop)
		total   int
		raw     [][12]float64
		silent  []bool
		t       = newTransform()
	)
	for {
		n, err := io.ReadFull(br, chunk)
		for i := 0; i+1 < n; i += 2 {
			pending = append(pending, float64(int16(binary.LittleEndian.Uint16(chunk[i:])))/32768) //nolint:gosec // signed 16-bit sample
		}
		total += n / 2
		for len(pending) >= frameSize {
			c, quiet := t.chroma(pending[:frameSize])
			raw = append(raw, c)
			silent = append(silent, quiet)
			pending = append(pending[:0], pending[hop:]...)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return Print{}, fmt.Errorf("audio fingerprint: %w", err)
		}
	}
	p := Print{
		Start: start, Length: time.Duration(total) * time.Second / SampleRate,
		Codes: make([]uint32, len(raw)), Silent: silent,
	}
	for i := range raw {
		c, ok := smoothed(raw, i)
		if !ok {
			p.Silent[i] = true
			continue
		}
		p.Codes[i] = code(c)
	}
	return p, nil
}

// smoothed smooths the chroma of frame i over its neighbors (filter 1/4, 3/4, 1, 3/4, 1/4), then
// scales it to unit norm. ok is false if there is no energy.
func smoothed(raw [][12]float64, i int) ([12]float64, bool) {
	weights := [5]float64{0.25, 0.75, 1, 0.75, 0.25}
	var c [12]float64
	for k, w := range weights {
		j := i + k - 2
		if j < 0 || j >= len(raw) {
			continue
		}
		for b := range c {
			c[b] += w * raw[j][b]
		}
	}
	var norm float64
	for _, v := range c {
		norm += v * v
	}
	if norm == 0 {
		return c, false
	}
	norm = math.Sqrt(norm)
	for b := range c {
		c[b] /= norm
	}
	return c, true
}

// code packs a normalized chroma into 32 bits, each one a comparison:
//   - 12 bits: each note against the next one;
//   - 12 bits: each pair of neighboring notes against the opposite pair on the circle of notes
//     (sums, which are less sensitive to noise than a single note);
//   - 8 bits: the first 8 notes against the level of a flat chroma.
func code(c [12]float64) uint32 {
	var x uint32
	bit := 0
	set := func(v bool) {
		if v {
			x |= 1 << bit
		}
		bit++
	}
	for b := range 12 {
		set(c[b] > c[(b+1)%12])
	}
	for b := range 12 {
		set(c[b]+c[(b+1)%12] > c[(b+6)%12]+c[(b+7)%12])
	}
	uniform := 1 / math.Sqrt(12)
	for b := range 8 {
		set(c[b] > uniform)
	}
	return x
}
