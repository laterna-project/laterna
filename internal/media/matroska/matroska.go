// Package matroska reads what a Matroska file (MKV, WebM) announces ahead of its data, without
// going through the file: the keyframe index (Cues) and the attachments (fonts for ASS subtitles).
// It reads a few kilobytes, wherever the file is (local disk, network share).
package matroska

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"time"
)

var (
	// ErrNotMatroska means the file is not Matroska, or its header cannot be read.
	ErrNotMatroska = errors.New("matroska: unrecognised file")
	// ErrNoIndex means there is no usable keyframe index (no Cues, no video track).
	ErrNoIndex = errors.New("no keyframe index in the file")
)

// EBML IDs (Matroska) we need.
const (
	idEBML             = 0x1A45DFA3
	idSegment          = 0x18538067
	idSeekHead         = 0x114D9B74
	idSeek             = 0x4DBB
	idSeekID           = 0x53AB
	idSeekPosition     = 0x53AC
	idInfo             = 0x1549A966
	idTimestampScale   = 0x2AD7B1
	idTracks           = 0x1654AE6B
	idTrackEntry       = 0xAE
	idTrackNumber      = 0xD7
	idTrackType        = 0x83
	idCues             = 0x1C53BB6B
	idCuePoint         = 0xBB
	idCueTime          = 0xB3
	idCueTrackPos      = 0xB7
	idCueTrack         = 0xF7
	idCluster          = 0x1F43B675
	idAttachments      = 0x1941A469
	idAttachedFile     = 0x61A7
	idFileName         = 0x466E
	idFileMimeType     = 0x4660
	idFileDescription  = 0x467E
	idFileData         = 0x465C
	trackTypeVideo     = 1
	unknownSize        = math.MaxUint64
	maxMetadataLength  = 64 << 20 // Info, Tracks, Cues: capped, as a guard against a malicious file
	maxAttachmentField = 64 << 10 // name, type and description of an attachment
)

// File is an open Matroska file: its leading elements are read, the others are located.
type File struct {
	file     *os.File
	r        *ebml
	segStart int64
	scale    uint64
	video    uint64
	cues     []byte
	cuesAt   int64
	// attachments is the position of the Attachments element (-1 if none).
	attachments int64
}

// Open reads the header of a Matroska file and the elements that come before its data. Those
// further away (Cues, attachments) are located through the SeekHead. It returns ErrNotMatroska if
// the file is not Matroska.
func Open(path string) (*File, error) {
	f, err := os.Open(path) //nolint:gosec // library file, path taken from the database
	if err != nil {
		return nil, err
	}
	m := &File{file: f, r: &ebml{f: f}, scale: 1_000_000, cuesAt: -1, attachments: -1}
	if err := m.readHead(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return m, nil
}

// Close closes the file.
func (m *File) Close() error { return m.file.Close() }

func (m *File) readHead() error {
	r := m.r
	id, size, err := r.header()
	if err != nil || id != idEBML {
		return ErrNotMatroska
	}
	if err := r.skip(size); err != nil {
		return ErrNotMatroska
	}
	id, size, err = r.header()
	if err != nil || id != idSegment {
		return ErrNotMatroska
	}
	m.segStart = r.pos
	segEnd := int64(math.MaxInt64)
	if end, ok := add(m.segStart, size); ok {
		segEnd = end
	}
	// Top-level elements up to the first Cluster (the data). The others are found through the
	// SeekHead.
	for r.pos < segEnd {
		start := r.pos
		id, size, err := r.header()
		if err != nil {
			break
		}
		switch id {
		case idSeekHead, idInfo, idTracks, idCues:
			body, err := r.body(size)
			if err != nil {
				return ErrNotMatroska
			}
			switch id {
			case idSeekHead:
				if at, ok := m.seekTarget(body, idCues); ok {
					m.cuesAt = at
				}
				if at, ok := m.seekTarget(body, idAttachments); ok {
					m.attachments = at
				}
			case idInfo:
				if v, ok := child(body, idTimestampScale); ok && v > 0 {
					m.scale = v
				}
			case idTracks:
				m.video = videoTrack(body)
			case idCues:
				m.cues = body
			}
		case idCluster:
			return nil // the data starts here: stop
		default:
			if id == idAttachments {
				m.attachments = start
			}
			if size < 0 {
				return nil
			}
			if err := r.skip(size); err != nil {
				return ErrNotMatroska
			}
		}
		if r.pos <= start {
			break
		}
	}
	//nolint:nilerr // end of the readable elements: what was read is enough (Cues and attachments are optional)
	return nil
}

// seekTarget returns the absolute position of an element from a SeekHead.
func (m *File) seekTarget(seekHead []byte, id uint64) (int64, bool) {
	p, ok := seekPosition(seekHead, id)
	if !ok {
		return 0, false
	}
	off, ok := toInt64(p)
	if !ok {
		return 0, false
	}
	return add(m.segStart, off)
}

// Keyframes returns the keyframes of the first video track from the Cues (presentation times,
// sorted, deduplicated). It returns ErrNoIndex if there are no Cues or no video track.
func (m *File) Keyframes() ([]time.Duration, error) {
	cues := m.cues
	if cues == nil && m.cuesAt >= 0 {
		m.r.pos = m.cuesAt
		id, size, err := m.r.header()
		if err == nil && id == idCues {
			if cues, err = m.r.body(size); err != nil {
				return nil, ErrNoIndex
			}
		}
	}
	if cues == nil || m.video == 0 {
		return nil, ErrNoIndex
	}
	times := cueTimes(cues, m.video, m.scale)
	if len(times) == 0 {
		return nil, ErrNoIndex
	}
	return times, nil
}

// Attachment is an attachment (font, image) of a Matroska file.
type Attachment struct {
	Name, MimeType, Description string
	// Size of the data.
	Size   int64
	offset int64
}

// Attachments lists the attachments without reading their data (see Data).
func (m *File) Attachments() ([]Attachment, error) {
	if m.attachments < 0 {
		return nil, nil
	}
	r := m.r
	r.pos = m.attachments
	id, size, err := r.header()
	if err != nil || id != idAttachments || size < 0 {
		return nil, fmt.Errorf("matroska: unreadable attachments at %d", m.attachments)
	}
	end, ok := add(r.pos, size)
	if !ok {
		return nil, ErrNotMatroska
	}
	var out []Attachment
	for r.pos < end {
		id, size, err := r.header()
		if err != nil || size < 0 {
			return nil, fmt.Errorf("matroska: unreadable attachment: %w", err)
		}
		fileEnd, ok := add(r.pos, size)
		if !ok || fileEnd > end {
			return nil, ErrNotMatroska
		}
		if id == idAttachedFile {
			a, err := m.attachedFile(fileEnd)
			if err != nil {
				return nil, err
			}
			out = append(out, a)
		}
		r.pos = fileEnd
	}
	return out, nil
}

// attachedFile reads the fields of an AttachedFile that runs up to end. The data is only located.
func (m *File) attachedFile(end int64) (Attachment, error) {
	r := m.r
	var a Attachment
	a.offset = -1
	for r.pos < end {
		id, size, err := r.header()
		if err != nil || size < 0 || size > end-r.pos {
			return a, fmt.Errorf("matroska: unreadable attachment at %d", r.pos)
		}
		switch id {
		case idFileData:
			a.offset, a.Size = r.pos, size
			r.pos += size
		case idFileName, idFileMimeType, idFileDescription:
			if size > maxAttachmentField {
				return a, fmt.Errorf("matroska: attachment field too long (%d bytes)", size)
			}
			b, err := r.body(size)
			if err != nil {
				return a, err
			}
			switch id {
			case idFileName:
				a.Name = string(b)
			case idFileMimeType:
				a.MimeType = string(b)
			default:
				a.Description = string(b)
			}
		default:
			r.pos += size
		}
	}
	if a.offset < 0 {
		return a, errors.New("matroska: attachment without data")
	}
	return a, nil
}

// Data returns the data of an attachment.
func (m *File) Data(a Attachment) io.Reader { return io.NewSectionReader(m.file, a.offset, a.Size) }

// ebml reads EBML elements at a given position.
type ebml struct {
	f   io.ReaderAt
	pos int64
}

// header reads the ID and size of the element at the current position. The size is -1 if unknown
// (an element left open until the end of its parent).
func (r *ebml) header() (id uint64, size int64, err error) {
	id, n, err := r.vint(true)
	if err != nil {
		return 0, 0, err
	}
	r.pos += int64(n)
	raw, n, err := r.vint(false)
	if err != nil {
		return 0, 0, err
	}
	r.pos += int64(n)
	if raw == unknownSize {
		return id, -1, nil
	}
	size, ok := toInt64(raw)
	if !ok {
		return 0, 0, fmt.Errorf("matroska: invalid element size at %d", r.pos)
	}
	return id, size, nil
}

// toInt64 converts an unsigned EBML value; false if it does not fit an int64.
func toInt64(v uint64) (int64, bool) {
	if v > math.MaxInt64 {
		return 0, false
	}
	return int64(v), true
}

// add adds two positions; false on overflow.
func add(a, b int64) (int64, bool) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

// vint reads a variable-length integer. keepMarker keeps the length bit (for IDs).
func (r *ebml) vint(keepMarker bool) (uint64, int, error) {
	var b [8]byte
	if _, err := r.f.ReadAt(b[:1], r.pos); err != nil {
		return 0, 0, err
	}
	n := 1
	for mask := byte(0x80); n <= 8 && b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 || (keepMarker && n > 4) {
		return 0, 0, fmt.Errorf("matroska: invalid EBML integer at %d", r.pos)
	}
	if n > 1 {
		if _, err := r.f.ReadAt(b[1:n], r.pos+1); err != nil {
			return 0, 0, err
		}
	}
	v := uint64(b[0])
	if !keepMarker {
		v &= 0xFF >> n
	}
	allOnes := v == uint64(0xFF>>n)
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
		allOnes = allOnes && b[i] == 0xFF
	}
	if !keepMarker && allOnes {
		return unknownSize, n, nil
	}
	return v, n, nil
}

func (r *ebml) skip(size int64) error {
	pos, ok := add(r.pos, size)
	if !ok {
		return ErrNotMatroska
	}
	r.pos = pos
	return nil
}

func (r *ebml) body(size int64) ([]byte, error) {
	if size < 0 || size > maxMetadataLength {
		return nil, fmt.Errorf("matroska: element of %d bytes not accepted", size)
	}
	b := make([]byte, size)
	if _, err := r.f.ReadAt(b, r.pos); err != nil {
		return nil, err
	}
	r.pos += size
	return b, nil
}

// elements iterates over the child elements of an EBML body.
func elements(body []byte, fn func(id uint64, data []byte) bool) {
	r := &ebml{f: bytesReaderAt(body)}
	for r.pos < int64(len(body)) {
		id, size, err := r.header()
		if err != nil || size < 0 || size > int64(len(body))-r.pos {
			return
		}
		if !fn(id, body[r.pos:r.pos+size]) {
			return
		}
		r.pos += size
	}
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func uintOf(data []byte) uint64 {
	var v uint64
	for _, c := range data {
		v = v<<8 | uint64(c)
	}
	return v
}

// child returns the integer value of the first child with that ID.
func child(body []byte, id uint64) (uint64, bool) {
	var v uint64
	found := false
	elements(body, func(cid uint64, data []byte) bool {
		if cid == id && len(data) <= 8 {
			v, found = uintOf(data), true
			return false
		}
		return true
	})
	return v, found
}

// seekPosition looks up the position (relative to the segment) of an element in a SeekHead.
func seekPosition(seekHead []byte, target uint64) (uint64, bool) {
	var pos uint64
	found := false
	elements(seekHead, func(id uint64, seek []byte) bool {
		if id != idSeek {
			return true
		}
		var sid, spos uint64
		elements(seek, func(cid uint64, data []byte) bool {
			switch cid {
			case idSeekID:
				sid = uintOf(data)
			case idSeekPosition:
				spos = uintOf(data)
			}
			return true
		})
		if sid == target {
			pos, found = spos, true
			return false
		}
		return true
	})
	return pos, found
}

// videoTrack returns the number of the first video track (0 if there is none).
func videoTrack(tracks []byte) uint64 {
	var number uint64
	elements(tracks, func(id uint64, entry []byte) bool {
		if id != idTrackEntry {
			return true
		}
		n, _ := child(entry, idTrackNumber)
		if t, _ := child(entry, idTrackType); t == trackTypeVideo {
			number = n
			return false
		}
		return true
	})
	return number
}

// cueTimes collects the times of the cue points of the video track, sorted and deduplicated.
func cueTimes(cues []byte, track, scale uint64) []time.Duration {
	var out []time.Duration
	elements(cues, func(id uint64, point []byte) bool {
		if id != idCuePoint {
			return true
		}
		var t uint64
		hasTime, forTrack := false, false
		elements(point, func(cid uint64, data []byte) bool {
			switch cid {
			case idCueTime:
				t, hasTime = uintOf(data), true
			case idCueTrackPos:
				if n, _ := child(data, idCueTrack); n == track {
					forTrack = true
				}
			}
			return true
		})
		if ns, ok := toInt64(t * scale); ok && hasTime && forTrack && t < math.MaxInt64/scale {
			out = append(out, time.Duration(ns))
		}
		return true
	})
	slices.Sort(out)
	return slices.Compact(out)
}
