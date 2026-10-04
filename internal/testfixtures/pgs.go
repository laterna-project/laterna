package testfixtures

import (
	"encoding/binary"
	"time"
)

// pgsEvent is one line of a test PGS subtitle: a white rectangle shown from start to end.
type pgsEvent struct {
	start, end time.Duration
	x, y, w, h int
}

// PGS segment types.
const (
	pgsPalette     = 0x14
	pgsObject      = 0x15
	pgsComposition = 0x16
	pgsWindow      = 0x17
	pgsEnd         = 0x80
)

// pgs writes a PGS subtitle (Blu-ray, ".sup") drawn on a width x height canvas. FFmpeg cannot
// produce one from text, so we write it by hand (a plain rectangle is enough to check display and
// burn-in).
func pgs(width, height int, events []pgsEvent) []byte {
	var out []byte
	seg := func(pts time.Duration, typ byte, payload []byte) {
		out = append(out, 'P', 'G')
		out = binary.BigEndian.AppendUint32(out, uint32(pts*90_000/time.Second)) //nolint:gosec // bounded test times
		out = binary.BigEndian.AppendUint32(out, 0)                              // DTS
		out = append(out, typ)
		out = binary.BigEndian.AppendUint16(out, uint16(len(payload))) //nolint:gosec // short test segments
		out = append(out, payload...)
	}
	u16 := func(b []byte, v int) []byte { return binary.BigEndian.AppendUint16(b, uint16(v)) } //nolint:gosec // test sizes
	// Empty composition at 0 s: the stream starts at zero (otherwise, once in a Matroska file,
	// FFmpeg would shift it so that its first line is there).
	start := u16(u16(nil, width), height)
	start = append(start, 0x10, 0, 0, 0x80, 0, 0, 0)
	seg(0, pgsComposition, start)
	seg(0, pgsEnd, nil)
	composition := 1
	for _, e := range events {
		// Display: composition (epoch start, one object), window, palette, object, end.
		pcs := u16(u16(nil, width), height)
		pcs = append(pcs, 0x10)
		pcs = u16(pcs, composition)
		pcs = append(pcs, 0x80, 0, 0, 1)
		pcs = u16(pcs, 0)
		pcs = append(pcs, 0, 0)
		pcs = u16(u16(pcs, e.x), e.y)
		window := u16(u16(u16(u16([]byte{1, 0}, e.x), e.y), e.w), e.h)
		palette := []byte{0, 0, 0, 16, 128, 128, 0, 1, 235, 128, 128, 255} // 0 transparent, 1 white
		var rle []byte
		for range e.h {
			for left := e.w; left > 0; {
				n := min(left, 16383)
				if n < 64 {
					rle = append(rle, 0, 0x80|byte(n), 1)
				} else {
					rle = append(rle, 0, 0xC0|byte(n>>8), byte(n), 1) //nolint:gosec // n < 16384
				}
				left -= n
			}
			rle = append(rle, 0, 0)
		}
		ods := u16(nil, 0)
		ods = append(ods, 0, 0xC0)
		length := len(rle) + 4
		ods = append(ods, byte(length>>16), byte(length>>8), byte(length)) //nolint:gosec // 3-byte length
		ods = u16(u16(ods, e.w), e.h)
		ods = append(ods, rle...)
		seg(e.start, pgsComposition, pcs)
		seg(e.start, pgsWindow, window)
		seg(e.start, pgsPalette, palette)
		seg(e.start, pgsObject, ods)
		seg(e.start, pgsEnd, nil)
		composition++

		// Clear: composition without an object.
		wipe := u16(u16(nil, width), height)
		wipe = append(wipe, 0x10)
		wipe = u16(wipe, composition)
		wipe = append(wipe, 0x00, 0, 0, 0)
		seg(e.end, pgsComposition, wipe)
		seg(e.end, pgsWindow, window)
		seg(e.end, pgsEnd, nil)
		composition++
	}
	return out
}
