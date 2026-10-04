// Package exif reads the EXIF data of a photo: date taken, orientation, camera, settings, location.
// No dependency: the TIFF structure is read directly from a JPEG (APP1 segment), a PNG (eXIf chunk)
// or a WebP (EXIF chunk).
package exif

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Info is what we keep from the EXIF data of a photo. An empty field says nothing.
type Info struct {
	// Orientation is 1 to 8 (1: as is, 6: rotate 90 degrees clockwise...); 0 if unknown.
	Orientation int
	// DateTimeOriginal is "2006:01:02 15:04:05" in camera time. OffsetTime is the camera's time
	// zone ("+02:00") when given.
	DateTimeOriginal string
	OffsetTime       string
	Make, Model      string
	Lens             string
	// FNumber is the aperture (f/...), FocalLength is in mm; 0 if unknown.
	FNumber, FocalLength float64
	// ExposureTime in seconds, as "1/250" or "2".
	ExposureTime string
	ISO          int
	// Latitude and Longitude in degrees (south and west are negative); nil if unknown.
	Latitude, Longitude *float64
}

// ErrNone means the file has no EXIF data.
var ErrNone = errors.New("no EXIF data")

// Limits against a malicious file.
const (
	maxSegment = 1 << 20 // bytes read from an EXIF block
	maxEntries = 512     // entries in a TIFF directory
	maxChunks  = 4096    // chunks walked in a PNG or a WebP
)

// ReadFile reads the EXIF data of a photo (JPEG, PNG, WebP); ErrNone if there is none.
func ReadFile(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = f.Close() }()
	var tiff []byte
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		tiff, err = fromJPEG(bufio.NewReader(f))
	case ".png":
		tiff, err = fromPNG(f)
	case ".webp":
		tiff, err = fromWebP(f)
	default:
		return Info{}, ErrNone
	}
	if err != nil {
		return Info{}, err
	}
	return Parse(tiff)
}

// fromJPEG finds the "Exif" APP1 segment of a JPEG, before the image data.
func fromJPEG(r *bufio.Reader) ([]byte, error) {
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi != [2]byte{0xFF, 0xD8} {
		return nil, ErrNone
	}
	for range 64 {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil || hdr[0] != 0xFF {
			return nil, ErrNone
		}
		marker := hdr[1]
		if marker == 0xDA || marker == 0xD9 { // start of scan, or end of image
			return nil, ErrNone
		}
		n := int(binary.BigEndian.Uint16(hdr[2:])) - 2
		if n < 0 {
			return nil, ErrNone
		}
		if marker == 0xE1 && n > 6 {
			data := make([]byte, n)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, err
			}
			if bytes.HasPrefix(data, []byte("Exif\x00\x00")) {
				return data[6:], nil
			}
			continue
		}
		if _, err := r.Discard(n); err != nil {
			return nil, ErrNone
		}
	}
	return nil, ErrNone
}

// fromPNG finds the eXIf chunk of a PNG.
func fromPNG(r io.ReadSeeker) ([]byte, error) {
	var sig [8]byte
	if _, err := io.ReadFull(r, sig[:]); err != nil || string(sig[:]) != "\x89PNG\r\n\x1a\n" {
		return nil, ErrNone
	}
	for range maxChunks {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, ErrNone
		}
		n := int64(binary.BigEndian.Uint32(hdr[:4]))
		switch string(hdr[4:]) {
		case "eXIf":
			if n > maxSegment {
				return nil, ErrNone
			}
			data := make([]byte, n)
			_, err := io.ReadFull(r, data)
			return data, err
		case "IEND":
			return nil, ErrNone
		}
		if _, err := r.Seek(n+4, io.SeekCurrent); err != nil { // data and CRC
			return nil, ErrNone
		}
	}
	return nil, ErrNone
}

// fromWebP finds the EXIF chunk of a WebP (extended format).
func fromWebP(r io.ReadSeeker) ([]byte, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || string(hdr[:4]) != "RIFF" || string(hdr[8:]) != "WEBP" {
		return nil, ErrNone
	}
	for range maxChunks {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, ErrNone
		}
		n := int64(binary.LittleEndian.Uint32(ch[4:]))
		if string(ch[:4]) == "EXIF" {
			if n > maxSegment {
				return nil, ErrNone
			}
			data := make([]byte, n)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, err
			}
			// Some tools keep the "Exif\0\0" header from JPEG.
			return bytes.TrimPrefix(data, []byte("Exif\x00\x00")), nil
		}
		if _, err := r.Seek(n+n%2, io.SeekCurrent); err != nil { // chunks are aligned on 2 bytes
			return nil, ErrNone
		}
	}
	return nil, ErrNone
}

// Tags we read.
const (
	tagMake             = 0x010F
	tagModel            = 0x0110
	tagOrientation      = 0x0112
	tagDateTime         = 0x0132
	tagExifIFD          = 0x8769
	tagGPSIFD           = 0x8825
	tagExposureTime     = 0x829A
	tagFNumber          = 0x829D
	tagISO              = 0x8827
	tagDateTimeOriginal = 0x9003
	tagOffsetTime       = 0x9010
	tagOffsetOriginal   = 0x9011
	tagFocalLength      = 0x920A
	tagLensModel        = 0xA434
	tagGPSLatitudeRef   = 0x0001
	tagGPSLatitude      = 0x0002
	tagGPSLongitudeRef  = 0x0003
	tagGPSLongitude     = 0x0004
)

// tiff reads a TIFF structure (header and directories).
type tiff struct {
	b  []byte
	bo binary.ByteOrder
}

type entry struct {
	tag, typ uint16
	count    uint32
	value    []byte // data of the entry (inline or at its offset)
}

// Parse reads the EXIF data of a TIFF structure (the content of an EXIF block).
func Parse(b []byte) (Info, error) {
	if len(b) < 8 {
		return Info{}, ErrNone
	}
	t := tiff{b: b}
	switch string(b[:2]) {
	case "II":
		t.bo = binary.LittleEndian
	case "MM":
		t.bo = binary.BigEndian
	default:
		return Info{}, ErrNone
	}
	if t.bo.Uint16(b[2:]) != 42 {
		return Info{}, ErrNone
	}
	var info Info
	ifd0, err := t.ifd(t.bo.Uint32(b[4:]))
	if err != nil {
		return Info{}, err
	}
	var dateTime string
	for _, e := range ifd0 {
		switch e.tag {
		case tagMake:
			info.Make = t.ascii(e)
		case tagModel:
			info.Model = t.ascii(e)
		case tagOrientation:
			if o := t.uint(e); o >= 1 && o <= 8 {
				info.Orientation = o
			}
		case tagDateTime:
			dateTime = t.ascii(e)
		case tagExifIFD:
			sub, err := t.ifd(uint32(t.uint(e))) //nolint:gosec // offset read from the file, checked by ifd
			if err != nil {
				continue
			}
			t.exifIFD(sub, &info)
		case tagGPSIFD:
			sub, err := t.ifd(uint32(t.uint(e))) //nolint:gosec // offset read from the file, checked by ifd
			if err != nil {
				continue
			}
			t.gpsIFD(sub, &info)
		}
	}
	if info.DateTimeOriginal == "" {
		info.DateTimeOriginal = dateTime
	}
	return info, nil
}

func (t tiff) exifIFD(entries []entry, info *Info) {
	var offset string
	for _, e := range entries {
		switch e.tag {
		case tagDateTimeOriginal:
			info.DateTimeOriginal = t.ascii(e)
		case tagOffsetOriginal:
			info.OffsetTime = t.ascii(e)
		case tagOffsetTime:
			offset = t.ascii(e)
		case tagExposureTime:
			if num, den, ok := t.rational(e, 0); ok && den != 0 {
				switch {
				case num >= den:
					info.ExposureTime = strconv.FormatFloat(float64(num)/float64(den), 'f', -1, 64)
				case num != 0:
					info.ExposureTime = fmt.Sprintf("1/%d", int(math.Round(float64(den)/float64(num))))
				}
			}
		case tagFNumber:
			info.FNumber = t.float(e)
		case tagFocalLength:
			info.FocalLength = t.float(e)
		case tagISO:
			info.ISO = t.uint(e)
		case tagLensModel:
			info.Lens = t.ascii(e)
		}
	}
	if info.OffsetTime == "" {
		info.OffsetTime = offset
	}
}

func (t tiff) gpsIFD(entries []entry, info *Info) {
	var latRef, lonRef string
	var lat, lon *float64
	for _, e := range entries {
		switch e.tag {
		case tagGPSLatitudeRef:
			latRef = t.ascii(e)
		case tagGPSLongitudeRef:
			lonRef = t.ascii(e)
		case tagGPSLatitude:
			lat = t.degrees(e)
		case tagGPSLongitude:
			lon = t.degrees(e)
		}
	}
	if lat == nil || lon == nil || math.Abs(*lat) > 90 || math.Abs(*lon) > 180 || (*lat == 0 && *lon == 0) {
		return
	}
	if latRef == "S" {
		*lat = -*lat
	}
	if lonRef == "W" {
		*lon = -*lon
	}
	info.Latitude, info.Longitude = lat, lon
}

// Size in bytes of each TIFF type.
var typeSizes = map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8}

// ifd reads a directory at offset off.
func (t tiff) ifd(off uint32) ([]entry, error) {
	b := t.b
	if int64(off)+2 > int64(len(b)) {
		return nil, ErrNone
	}
	n := int(t.bo.Uint16(b[off:]))
	if n > maxEntries || int64(off)+2+int64(n)*12 > int64(len(b)) {
		return nil, ErrNone
	}
	out := make([]entry, 0, n)
	for i := range n {
		p := int(off) + 2 + i*12
		e := entry{tag: t.bo.Uint16(b[p:]), typ: t.bo.Uint16(b[p+2:]), count: t.bo.Uint32(b[p+4:])}
		size, ok := typeSizes[e.typ]
		if !ok || e.count > maxSegment {
			continue
		}
		total := size * int(e.count)
		if total <= 4 {
			e.value = b[p+8 : p+8+total]
		} else {
			at := int64(t.bo.Uint32(b[p+8:]))
			if at+int64(total) > int64(len(b)) {
				continue
			}
			e.value = b[at : at+int64(total)]
		}
		out = append(out, e)
	}
	return out, nil
}

func (t tiff) ascii(e entry) string {
	s := string(bytes.TrimRight(e.value, "\x00 "))
	return strings.TrimSpace(strings.ToValidUTF8(s, ""))
}

func (t tiff) uint(e entry) int {
	switch {
	case e.typ == 3 && len(e.value) >= 2:
		return int(t.bo.Uint16(e.value))
	case e.typ == 4 && len(e.value) >= 4:
		return int(t.bo.Uint32(e.value))
	}
	return 0
}

func (t tiff) rational(e entry, i int) (num, den uint32, ok bool) {
	if (e.typ != 5 && e.typ != 10) || len(e.value) < 8*(i+1) {
		return 0, 0, false
	}
	return t.bo.Uint32(e.value[8*i:]), t.bo.Uint32(e.value[8*i+4:]), true
}

func (t tiff) float(e entry) float64 {
	num, den, ok := t.rational(e, 0)
	if !ok || den == 0 {
		return 0
	}
	return math.Round(float64(num)/float64(den)*100) / 100
}

// degrees reads three rationals (degrees, minutes, seconds).
func (t tiff) degrees(e entry) *float64 {
	var v float64
	for i, div := range []float64{1, 60, 3600} {
		num, den, ok := t.rational(e, i)
		if !ok || den == 0 {
			return nil
		}
		v += float64(num) / float64(den) / div
	}
	return &v
}

// TakenAt returns when the picture was taken: exact if the camera recorded its time zone (offset in
// minutes), otherwise the camera's clock read in loc. ok is false without a readable date.
func (i Info) TakenAt(loc *time.Location) (t time.Time, offset *int, ok bool) {
	raw := strings.TrimSpace(i.DateTimeOriginal)
	if len(raw) < len("2006:01:02 15:04:05") || strings.HasPrefix(raw, "0000") {
		return time.Time{}, nil, false
	}
	raw = raw[:19]
	if off := strings.TrimSpace(i.OffsetTime); len(off) == 6 {
		if t, err := time.Parse("2006:01:02 15:04:05-07:00", raw+off); err == nil {
			_, secs := t.Zone()
			minutes := secs / 60
			return t, &minutes, true
		}
	}
	t, err := time.ParseInLocation("2006:01:02 15:04:05", raw, loc)
	return t, nil, err == nil
}
