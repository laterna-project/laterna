package testfixtures

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"math"
	"os"
	"path/filepath"
	"time"
)

// Photos.
//
// Written in Go: JPEG and PNG, with EXIF data written here (TIFF structure).

// Exif describes the EXIF data of a test photo. An empty field is not written.
type Exif struct {
	// BigEndian writes the structure as "MM" ("II" otherwise).
	BigEndian        bool
	Orientation      int
	DateTimeOriginal string
	OffsetTime       string
	Make, Model      string
	Lens             string
	// FNumber, ExposureTime and FocalLength are fractions {numerator, denominator}.
	FNumber, ExposureTime, FocalLength [2]uint32
	ISO                                int
	// Latitude and Longitude in degrees (south and west are negative); 0 and 0 mean no location.
	Latitude, Longitude float64
}

type tiffEntry struct {
	tag, typ uint16
	count    uint32
	data     []byte
}

// TIFF builds the TIFF structure of the EXIF data.
func (e Exif) TIFF() []byte {
	var bo binary.ByteOrder = binary.LittleEndian
	head := []byte("II*\x00")
	if e.BigEndian {
		bo, head = binary.BigEndian, []byte("MM\x00*")
	}
	ascii := func(tag uint16, s string) []tiffEntry {
		if s == "" {
			return nil
		}
		return []tiffEntry{{tag: tag, typ: 2, count: uint32(len(s) + 1), data: append([]byte(s), 0)}} //nolint:gosec // short text
	}
	short := func(tag uint16, v int) []tiffEntry {
		if v == 0 {
			return nil
		}
		b := make([]byte, 2)
		bo.PutUint16(b, uint16(v)) //nolint:gosec // small test value
		return []tiffEntry{{tag: tag, typ: 3, count: 1, data: b}}
	}
	rational := func(tag uint16, r ...[2]uint32) []tiffEntry {
		if len(r) == 0 || r[0][1] == 0 {
			return nil
		}
		b := make([]byte, 8*len(r))
		for i, x := range r {
			bo.PutUint32(b[8*i:], x[0])
			bo.PutUint32(b[8*i+4:], x[1])
		}
		return []tiffEntry{{tag: tag, typ: 5, count: uint32(len(r)), data: b}} //nolint:gosec // three values at most
	}
	pointer := func(tag uint16) tiffEntry { return tiffEntry{tag: tag, typ: 4, count: 1, data: make([]byte, 4)} }
	dms := func(v float64) [][2]uint32 {
		v = math.Abs(v)
		d := math.Floor(v)
		m := math.Floor((v - d) * 60)
		s := (v - d - m/60) * 3600
		return [][2]uint32{{uint32(d), 1}, {uint32(m), 1}, {uint32(math.Round(s * 1000)), 1000}}
	}

	var exifIFD []tiffEntry
	exifIFD = append(exifIFD, rational(0x829A, e.ExposureTime)...)
	exifIFD = append(exifIFD, rational(0x829D, e.FNumber)...)
	exifIFD = append(exifIFD, short(0x8827, e.ISO)...)
	exifIFD = append(exifIFD, ascii(0x9003, e.DateTimeOriginal)...)
	exifIFD = append(exifIFD, ascii(0x9011, e.OffsetTime)...)
	exifIFD = append(exifIFD, rational(0x920A, e.FocalLength)...)
	exifIFD = append(exifIFD, ascii(0xA434, e.Lens)...)
	var gps []tiffEntry
	if e.Latitude != 0 || e.Longitude != 0 {
		latRef, lonRef := "N", "E"
		if e.Latitude < 0 {
			latRef = "S"
		}
		if e.Longitude < 0 {
			lonRef = "W"
		}
		gps = append(gps, ascii(0x0001, latRef)...)
		gps = append(gps, rational(0x0002, dms(e.Latitude)...)...)
		gps = append(gps, ascii(0x0003, lonRef)...)
		gps = append(gps, rational(0x0004, dms(e.Longitude)...)...)
	}
	var ifd0 []tiffEntry
	ifd0 = append(ifd0, ascii(0x010F, e.Make)...)
	ifd0 = append(ifd0, ascii(0x0110, e.Model)...)
	ifd0 = append(ifd0, short(0x0112, e.Orientation)...)
	exifAt, gpsAt := -1, -1
	if len(exifIFD) > 0 {
		exifAt = len(ifd0)
		ifd0 = append(ifd0, pointer(0x8769))
	}
	if len(gps) > 0 {
		gpsAt = len(ifd0)
		ifd0 = append(ifd0, pointer(0x8825))
	}

	size := func(entries []tiffEntry) int {
		n := 2 + 12*len(entries) + 4
		for _, en := range entries {
			if len(en.data) > 4 {
				n += len(en.data) + len(en.data)%2
			}
		}
		return n
	}
	off0 := 8
	offExif := off0 + size(ifd0)
	offGPS := offExif
	if len(exifIFD) > 0 {
		offGPS += size(exifIFD)
	}
	if exifAt >= 0 {
		bo.PutUint32(ifd0[exifAt].data, uint32(offExif)) //nolint:gosec // small test file
	}
	if gpsAt >= 0 {
		bo.PutUint32(ifd0[gpsAt].data, uint32(offGPS)) //nolint:gosec // small test file
	}
	write := func(out []byte, entries []tiffEntry, at int) []byte {
		b := make([]byte, 2+12*len(entries)+4)
		bo.PutUint16(b, uint16(len(entries))) //nolint:gosec // a few entries
		data := at + len(b)
		var extra []byte
		for i, en := range entries {
			p := 2 + 12*i
			bo.PutUint16(b[p:], en.tag)
			bo.PutUint16(b[p+2:], en.typ)
			bo.PutUint32(b[p+4:], en.count)
			if len(en.data) <= 4 {
				copy(b[p+8:], en.data)
				continue
			}
			bo.PutUint32(b[p+8:], uint32(data+len(extra))) //nolint:gosec // small test file
			extra = append(extra, en.data...)
			if len(en.data)%2 == 1 {
				extra = append(extra, 0)
			}
		}
		return append(append(out, b...), extra...)
	}
	out := make([]byte, 0, 256)
	out = append(out, head...)
	out = append(out, 0, 0, 0, 0)
	bo.PutUint32(out[4:], 8) // first directory: right after the header (off0)
	out = write(out, ifd0, off0)
	if len(exifIFD) > 0 {
		out = write(out, exifIFD, offExif)
	}
	if len(gps) > 0 {
		out = write(out, gps, offGPS)
	}
	return out
}

// JPEGWithExif encodes an image as JPEG and adds the EXIF data (APP1 segment).
func JPEGWithExif(img image.Image, e Exif) []byte {
	raw := JPEG(img)
	tiff := e.TIFF()
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(2+6+len(tiff))) //nolint:gosec // under 64 KiB
	seg = append(append(seg, "Exif\x00\x00"...), tiff...)
	return append(append(append([]byte{}, raw[:2]...), seg...), raw[2:]...)
}

// PNGWithExif encodes an image as PNG and adds the EXIF data (eXIf chunk, after the header).
func PNGWithExif(img image.Image, e Exif) []byte {
	raw := pngBytes(img)
	tiff := e.TIFF()
	chunk := make([]byte, 8, 12+len(tiff))
	binary.BigEndian.PutUint32(chunk, uint32(len(tiff))) //nolint:gosec // small chunk
	copy(chunk[4:], "eXIf")
	chunk = append(chunk, tiff...)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(chunk[4:]))
	chunk = append(chunk, crc...)
	const afterIHDR = 8 + 25 // signature + IHDR chunk
	return append(append(append([]byte{}, raw[:afterIHDR]...), chunk...), raw[afterIHDR:]...)
}

// photos writes the photo library: nested albums (folders), a photo rotated by its orientation,
// dates with and without a time zone, a GPS location, a PNG with EXIF, a photo without a date (the
// file's is used), a NAS thumbnail folder to ignore.
func (g *generator) photos(context.Context) error {
	root := filepath.Join(g.root, "Photos")
	holidays := filepath.Join(root, "2024", "Holidays")
	files := []struct {
		path string
		data []byte
	}{
		{filepath.Join(holidays, "IMG_0001.jpg"), JPEGWithExif(PageImage(640, 480, 1), Exif{
			Orientation: 1, DateTimeOriginal: "2024:07:14 18:32:05", OffsetTime: "+02:00", Make: "Maker", Model: "Test Camera",
			Lens: "4.2 mm lens", FNumber: [2]uint32{18, 10}, ExposureTime: [2]uint32{1, 250}, ISO: 100,
			FocalLength: [2]uint32{42, 10}, Latitude: 48.3904, Longitude: -4.4861,
		})},
		// Stored sideways (640 x 480), to be rotated 90 degrees: 480 x 640 as displayed.
		{filepath.Join(holidays, "IMG_0002.jpg"), JPEGWithExif(PageImage(640, 480, 2), Exif{
			BigEndian: true, Orientation: 6, DateTimeOriginal: "2024:07:15 09:00:00", Make: "Maker", Model: "Test Camera",
		})},
		{filepath.Join(root, "2024", "Christmas.png"), PNGWithExif(PageImage(320, 240, 3), Exif{DateTimeOriginal: "2024:12:24 20:00:00"})},
		{filepath.Join(root, "2023", "IMG_9999.jpg"), JPEG(PageImage(320, 240, 4))},
		{filepath.Join(root, "@eaDir", "IMG_0001.jpg", "SYNOPHOTO_THUMB_S.jpg"), JPEG(PageImage(32, 24, 5))},
	}
	for _, f := range files {
		if err := g.bytes(f.path, f.data); err != nil {
			return err
		}
	}
	// No EXIF: the photo's date is the file's.
	undated := filepath.Join(root, "2023", "IMG_9999.jpg")
	when := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	return os.Chtimes(undated, when, when)
}
