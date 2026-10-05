package matroska

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/laterna-project/laterna/internal/testfixtures"
)

// The attached font is read without going through the file, and a file without attachments has
// none.
func TestAttachments(t *testing.T) {
	f, err := Open(testfixtures.Path(t, "Subtitles/Fonts (2022)/Fonts (2022).mkv"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	atts, err := f.Attachments()
	if err != nil || len(atts) != 1 || atts[0].Name != "GoRegular.ttf" || atts[0].MimeType != "application/x-truetype-font" {
		t.Fatalf("attachments: %+v %v", atts, err)
	}
	data, err := io.ReadAll(f.Data(atts[0]))
	if err != nil || !bytes.Equal(data, goregular.TTF) {
		t.Errorf("data: %d bytes, %v", len(data), err)
	}
	if times, err := f.Keyframes(); err != nil || len(times) == 0 {
		t.Errorf("keyframes: %v %v", times, err)
	}
	plain, err := Open(testfixtures.Path(t, "Movies/Dual Audio (2019)/Dual Audio (2019).mkv"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plain.Close() }()
	if atts, err := plain.Attachments(); err != nil || len(atts) != 0 {
		t.Errorf("no attachment: %+v %v", atts, err)
	}
}

func TestRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake.mkv")
	if err := os.WriteFile(path, []byte("not a matroska file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrNotMatroska) {
		t.Errorf("%v", err)
	}
}

// Real files (LATERNA_MATROSKA_REAL, paths separated by "|"): attachments read without going
// through the file.
func TestRealAttachments(t *testing.T) {
	paths := os.Getenv("LATERNA_MATROSKA_REAL")
	if paths == "" {
		t.Skip("LATERNA_MATROSKA_REAL is not set")
	}
	for _, path := range filepath.SplitList(paths) {
		start := time.Now()
		f, err := Open(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		atts, err := f.Attachments()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var total int64
		for _, a := range atts {
			n, err := io.Copy(io.Discard, f.Data(a))
			if err != nil || n != a.Size {
				t.Errorf("%s: %v (%d/%d)", a.Name, err, n, a.Size)
			}
			total += n
		}
		t.Logf("%s: %d attachments, %d bytes, %v", filepath.Base(path), len(atts), total, time.Since(start))
		for _, a := range atts[:min(3, len(atts))] {
			t.Logf("  %s (%s) %d bytes", a.Name, a.MimeType, a.Size)
		}
		_ = f.Close()
	}
}
