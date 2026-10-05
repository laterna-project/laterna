package exif

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadJPEG(t *testing.T) {
	for _, big := range []bool{false, true} {
		p := write(t, "photo.jpg", testfixtures.JPEGWithExif(testfixtures.PageImage(64, 48, 1), testfixtures.Exif{
			BigEndian: big, Orientation: 6, DateTimeOriginal: "2024:07:14 18:32:05", OffsetTime: "+02:00",
			Make: "Maker", Model: "Camera", Lens: "Lens", FNumber: [2]uint32{18, 10}, ExposureTime: [2]uint32{1, 250},
			ISO: 100, FocalLength: [2]uint32{42, 10}, Latitude: 48.3904, Longitude: -4.4861,
		}))
		info, err := ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Orientation != 6 || info.Make != "Maker" || info.Model != "Camera" || info.Lens != "Lens" ||
			info.FNumber != 1.8 || info.ExposureTime != "1/250" || info.ISO != 100 || info.FocalLength != 4.2 ||
			info.Latitude == nil || math.Abs(*info.Latitude-48.3904) > 1e-4 || info.Longitude == nil || math.Abs(*info.Longitude+4.4861) > 1e-4 {
			t.Fatalf("EXIF (MM=%v): %+v", big, info)
		}
		at, offset, ok := info.TakenAt(time.UTC)
		if !ok || offset == nil || *offset != 120 || !at.Equal(time.Date(2024, 7, 14, 16, 32, 5, 0, time.UTC)) {
			t.Errorf("date: %v %v %v", at, offset, ok)
		}
	}
}

func TestReadPNGAndWebP(t *testing.T) {
	png := write(t, "photo.png", testfixtures.PNGWithExif(testfixtures.PageImage(32, 24, 2), testfixtures.Exif{DateTimeOriginal: "2024:12:24 20:00:00"}))
	info, err := ReadFile(png)
	if err != nil {
		t.Fatal(err)
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	if paris == nil {
		paris = time.FixedZone("CET", 3600)
	}
	// No time zone: the camera's clock, read in the server's zone.
	at, offset, ok := info.TakenAt(paris)
	if !ok || offset != nil || !at.Equal(time.Date(2024, 12, 24, 19, 0, 0, 0, time.UTC)) {
		t.Errorf("PNG: %v %v %v", at, offset, ok)
	}

	// Extended WebP: VP8X chunk, then EXIF (here with the JPEG "Exif" header in front).
	tiff := testfixtures.Exif{Orientation: 3}.TIFF()
	chunk := func(kind string, data []byte) []byte {
		b := make([]byte, 8, 8+len(data)+1)
		copy(b, kind)
		binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
		b = append(b, data...)
		if len(data)%2 == 1 {
			b = append(b, 0)
		}
		return b
	}
	body := append([]byte("WEBP"), chunk("VP8X", make([]byte, 10))...)
	body = append(body, chunk("ALPH", []byte{1, 2, 3})...)
	body = append(body, chunk("EXIF", append([]byte("Exif\x00\x00"), tiff...))...)
	riff := append([]byte("RIFF\x00\x00\x00\x00"), body...)
	binary.LittleEndian.PutUint32(riff[4:], uint32(len(body)))
	info, err = ReadFile(write(t, "photo.webp", riff))
	if err != nil || info.Orientation != 3 {
		t.Errorf("WebP: %+v %v", info, err)
	}

	// No EXIF.
	if _, err := ReadFile(write(t, "bare.jpg", testfixtures.JPEG(testfixtures.PageImage(8, 8, 1)))); !errors.Is(err, ErrNone) {
		t.Errorf("JPEG without EXIF: %v", err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(testfixtures.Exif{Orientation: 6, DateTimeOriginal: "2024:07:14 18:32:05", Latitude: 1, Longitude: 2}.TIFF())
	f.Add(testfixtures.Exif{BigEndian: true, Make: "M", FNumber: [2]uint32{1, 0}}.TIFF())
	f.Fuzz(func(_ *testing.T, b []byte) {
		info, err := Parse(b)
		if err == nil {
			_, _, _ = info.TakenAt(time.UTC)
		}
	})
}
