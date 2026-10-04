// Package fmp4 reads a fragmented MP4 stream as FFmpeg writes it for HLS playback: the init segment
// (ftyp + moov), then fragments (moof + mdat), each one dated by the presentation time of its first
// video frame.
package fmp4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"time"
)

// maxBox caps the size of a box (a fragment of 4K keyframes stays well below).
const maxBox = 256 << 20

// Fragment is one fragment (moof then mdat, and whatever sits between them).
type Fragment struct {
	// Data holds the bytes of the fragment, ready to be served in a segment.
	Data []byte
	// Start is the presentation time of the first video frame of the fragment.
	Start time.Duration
	// moofLen is the length of the moof box at the start of Data.
	moofLen int
}

// Reader reads an fMP4 stream.
type Reader struct {
	r      io.Reader
	init   []byte
	video  uint32
	scales map[uint32]uint32
	// shifts holds the edit list offset of each track. With B-frames, composition is delayed by
	// that much and the edit list takes it off at display time.
	shifts map[uint32]int64
	next   []byte // box read ahead
}

// NewReader reads the init segment of the stream (ftyp, moov) and finds the video track.
func NewReader(r io.Reader) (*Reader, error) {
	rd := &Reader{r: r, scales: map[uint32]uint32{}, shifts: map[uint32]int64{}}
	for {
		typ, box, err := rd.box()
		if err != nil {
			return nil, fmt.Errorf("fmp4: incomplete init: %w", err)
		}
		if typ == "moof" {
			rd.next = box
			break
		}
		rd.init = append(rd.init, box...)
		if typ == "moov" {
			rd.readMoov(box)
		}
	}
	if rd.video == 0 {
		return nil, errors.New("fmp4: no video track")
	}
	return rd, nil
}

// Init returns the init segment (ftyp + moov), shared by all segments.
func (rd *Reader) Init() []byte { return rd.init }

// Next reads the next fragment; io.EOF at the end of the stream.
func (rd *Reader) Next() (Fragment, error) {
	moof := rd.next
	rd.next = nil
	// Other boxes between two fragments are skipped (mfra, the final index; free...).
	for moof == nil {
		typ, box, err := rd.box()
		if err != nil {
			return Fragment{}, err
		}
		if typ == "moof" {
			moof = box
		}
	}
	pts, ok := rd.presentation(moof)
	if !ok {
		return Fragment{}, errors.New("fmp4: fragment without a video track")
	}
	f := Fragment{Data: moof, Start: pts, moofLen: len(moof)}
	for {
		typ, box, err := rd.box()
		if errors.Is(err, io.EOF) {
			return Fragment{}, io.ErrUnexpectedEOF
		}
		if err != nil {
			return Fragment{}, err
		}
		f.Data = append(f.Data, box...)
		if typ == "mdat" {
			return f, nil
		}
	}
}

// Renumber sets the sequence number of the fragment (mfhd), so that a segment is byte-for-byte the
// same whichever FFmpeg run produced it.
func (f *Fragment) Renumber(seq uint32) {
	for c := range children(f.Data[:f.moofLen]) {
		if c.typ == "mfhd" && len(c.data) >= 16 {
			binary.BigEndian.PutUint32(f.Data[c.off+12:c.off+16], seq)
			return
		}
	}
}

// box reads a whole box.
func (rd *Reader) box() (string, []byte, error) {
	var head [8]byte
	if _, err := io.ReadFull(rd.r, head[:]); err != nil {
		return "", nil, err
	}
	size := uint64(binary.BigEndian.Uint32(head[:4]))
	typ := string(head[4:8])
	hl := 8
	var ext [8]byte
	if size == 1 {
		if _, err := io.ReadFull(rd.r, ext[:]); err != nil {
			return "", nil, err
		}
		size, hl = binary.BigEndian.Uint64(ext[:]), 16
	}
	if size < uint64(hl) || size > maxBox {
		return "", nil, fmt.Errorf("fmp4: box %q of size %d not accepted", typ, size)
	}
	box := make([]byte, size)
	copy(box, head[:])
	if hl == 16 {
		copy(box[8:], ext[:])
	}
	if _, err := io.ReadFull(rd.r, box[hl:]); err != nil {
		return "", nil, err
	}
	return typ, box, nil
}

// child is a child box: its position in the parent, its type and its bytes.
type child struct {
	off  int
	typ  string
	data []byte
}

// children iterates over the child boxes of box (after its 8-byte header).
func children(box []byte) iter.Seq[child] {
	return func(yield func(child) bool) {
		for i := 8; i+8 <= len(box); {
			size := int(binary.BigEndian.Uint32(box[i : i+4]))
			if size < 8 || i+size > len(box) {
				return
			}
			if !yield(child{off: i, typ: string(box[i+4 : i+8]), data: box[i : i+size]}) {
				return
			}
			i += size
		}
	}
}

// fullBoxVersion returns the version of a full box (first byte after the header).
func fullBoxVersion(box []byte) byte {
	if len(box) < 9 {
		return 0
	}
	return box[8]
}

// readMoov records the timescale of each track and finds the video track.
func (rd *Reader) readMoov(moov []byte) {
	for trak := range children(moov) {
		if trak.typ != "trak" {
			continue
		}
		var id, scale uint32
		var shift int64
		video := false
		for c := range children(trak.data) {
			b := c.data
			switch c.typ {
			case "tkhd":
				off := 20 // version 0: the ID comes after creation and modification times (4 bytes each)
				if fullBoxVersion(b) == 1 {
					off = 28
				}
				if len(b) >= off+4 {
					id = binary.BigEndian.Uint32(b[off : off+4])
				}
			case "edts":
				for c2 := range children(b) {
					if c2.typ == "elst" {
						shift = editShift(c2.data)
					}
				}
			case "mdia":
				for c2 := range children(b) {
					b2 := c2.data
					switch c2.typ {
					case "mdhd":
						off := 20
						if fullBoxVersion(b2) == 1 {
							off = 28
						}
						if len(b2) >= off+4 {
							scale = binary.BigEndian.Uint32(b2[off : off+4])
						}
					case "hdlr":
						video = len(b2) >= 20 && string(b2[16:20]) == "vide"
					}
				}
			}
		}
		rd.scales[id] = scale
		rd.shifts[id] = shift
		if video && rd.video == 0 {
			rd.video = id
		}
	}
}

// presentation computes the presentation time of the first video frame of a fragment: tfdt (decode
// time) + composition offset of the first sample (trun) - edit list offset (elst).
func (rd *Reader) presentation(moof []byte) (time.Duration, bool) {
	for traf := range children(moof) {
		if traf.typ != "traf" {
			continue
		}
		var track uint32
		var base int64
		var cto int64
		for c := range children(traf.data) {
			b := c.data
			switch c.typ {
			case "tfhd":
				if len(b) >= 16 {
					track = binary.BigEndian.Uint32(b[12:16])
				}
			case "tfdt":
				if fullBoxVersion(b) == 1 && len(b) >= 20 {
					base = int64(binary.BigEndian.Uint64(b[12:20])) //nolint:gosec // MP4 time: 63 useful bits
				} else if len(b) >= 16 {
					base = int64(binary.BigEndian.Uint32(b[12:16]))
				}
			case "trun":
				cto = firstCompositionOffset(b)
			}
		}
		scale := int64(rd.scales[track])
		if track == rd.video && scale > 0 {
			// In two steps (seconds, then the remainder) so that long durations do not overflow.
			v := base + cto - rd.shifts[track]
			return time.Duration(v/scale)*time.Second + time.Duration(v%scale*int64(time.Second)/scale), true
		}
	}
	return 0, false
}

// firstCompositionOffset reads the composition offset of the first sample of a trun.
func firstCompositionOffset(trun []byte) int64 {
	if len(trun) < 16 {
		return 0
	}
	version := trun[8]
	flags := uint32(trun[9])<<16 | uint32(trun[10])<<8 | uint32(trun[11])
	i := 16 // after version, flags and sample count
	for _, f := range []uint32{0x1, 0x4, 0x100, 0x200, 0x400} {
		if flags&f != 0 {
			i += 4
		}
	}
	if flags&0x800 == 0 || len(trun) < i+4 {
		return 0
	}
	v := binary.BigEndian.Uint32(trun[i : i+4])
	if version == 1 {
		return int64(int32(v)) //nolint:gosec // signed offset in version 1
	}
	return int64(v)
}

// editShift reads the media start of the first non-empty entry of an edit list (elst).
func editShift(elst []byte) int64 {
	if len(elst) < 16 {
		return 0
	}
	version := elst[8]
	count := binary.BigEndian.Uint32(elst[12:16])
	entry := 12 // duration (4), media start (4), rate (4)
	if version == 1 {
		entry = 20 // duration (8), media start (8), rate (4)
	}
	for i := range int(min(count, 16)) {
		off := 16 + i*entry
		if len(elst) < off+entry {
			return 0
		}
		var start int64
		if version == 1 {
			start = int64(binary.BigEndian.Uint64(elst[off+8 : off+16])) //nolint:gosec // signed value
		} else {
			start = int64(int32(binary.BigEndian.Uint32(elst[off+4 : off+8]))) //nolint:gosec // signed value
		}
		if start >= 0 { // -1: empty edit (a delay) with no media
			return start
		}
	}
	return 0
}
