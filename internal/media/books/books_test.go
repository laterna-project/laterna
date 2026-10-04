package books

import (
	"bytes"
	"errors"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/laterna-project/laterna/internal/domain"
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

func TestEPUB(t *testing.T) {
	cover := testfixtures.JPEG(testfixtures.PageImage(30, 45, 1))
	p := write(t, "livre.epub", testfixtures.EPUB(`<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title id="t">Titre</dc:title>
    <dc:creator id="a1">Autrice</dc:creator>
    <meta refines="#a1" property="role" scheme="marc:relators">aut</meta>
    <meta property="belongs-to-collection" id="c1">Série</meta>
    <meta refines="#c1" property="collection-type">series</meta>
    <meta refines="#c1" property="group-position">3</meta>
  </metadata>
  <manifest>
    <item id="img" href="Images/cover.jpg" media-type="image/jpeg" properties="cover-image"/>
  </manifest>
  <spine page-progression-direction="rtl"/>
</package>`, cover))
	info, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	m := info.Meta
	if info.Format != domain.BookEPUB || info.Layout != domain.LayoutReflowable || !info.RightToLeft || !info.HasCover ||
		m.Title != "Titre" || m.Series != "Série" || m.Number != 3 || !slices.Equal(m.Authors, []string{"Autrice"}) {
		t.Fatalf("EPUB: %+v", info)
	}
	data, ext, err := Cover(p)
	if err != nil || ext != ".jpg" || !bytes.Equal(data, cover) {
		t.Fatalf("cover: %s %v", ext, err)
	}
	if _, _, err := Page(p, 0); !errors.Is(err, ErrNoPage) {
		t.Errorf("page of an EPUB: %v", err)
	}
}

func TestCBZ(t *testing.T) {
	entries := []testfixtures.ZipEntry{
		{Name: "ComicInfo.xml", Data: []byte(`<ComicInfo><Series>Manga</Series><Number>2</Number><Manga>YesAndRightToLeft</Manga>
<Pages><Page Image="1" Type="FrontCover"/></Pages></ComicInfo>`)},
		{Name: "Ch 10/10.jpg", Data: testfixtures.JPEG(testfixtures.PageImage(20, 30, 3))},
		{Name: "Ch 10/2.jpg", Data: testfixtures.JPEG(testfixtures.PageImage(40, 30, 2))},
		{Name: "Ch 9/1.jpg", Data: testfixtures.JPEG(testfixtures.PageImage(10, 30, 1))},
		{Name: "__MACOSX/Ch 9/._1.jpg", Data: []byte("x")},
		{Name: ".DS_Store", Data: []byte("x")},
	}
	p := write(t, "tome.cbz", testfixtures.Zip(entries))
	info, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	// Natural order: chapter 9 before 10, page 2 before 10.
	want := []domain.PageSize{{Width: 10, Height: 30}, {Width: 40, Height: 30}, {Width: 20, Height: 30}}
	if info.Layout != domain.LayoutImages || info.PageCount != 3 || !slices.Equal(info.Pages, want) || !info.RightToLeft ||
		info.Meta.Series != "Manga" || info.Meta.Number != 2 {
		t.Fatalf("CBZ: %+v", info)
	}
	data, ext, err := Page(p, 2)
	if err != nil || ext != ".jpg" || !bytes.Equal(data, entries[1].Data) {
		t.Fatalf("page 2: %s %v", ext, err)
	}
	// Cover named by ComicInfo: the second image.
	if data, _, err := Cover(p); err != nil || !bytes.Equal(data, entries[2].Data) {
		t.Fatalf("cover: %v", err)
	}
	if _, _, err := Page(p, 3); !errors.Is(err, ErrNoPage) {
		t.Errorf("page outside the book: %v", err)
	}
}

func TestScannedPDF(t *testing.T) {
	var pages []testfixtures.PDFPage
	for i := range 3 {
		w := 40 + 40*(i/2) // the last one is a double page
		pages = append(pages, testfixtures.PDFPage{JPEG: testfixtures.JPEG(testfixtures.PageImage(w, 56, i)), Width: w, Height: 56})
	}
	p := write(t, "bd.pdf", testfixtures.ScannedPDF(pages, map[string]string{"Title": "Bande dessinée", "Author": "A & B", "CreationDate": "D:20190304"}))
	info, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.PageSize{{Width: 40, Height: 56}, {Width: 40, Height: 56}, {Width: 80, Height: 56}}
	if info.Layout != domain.LayoutImages || info.PageCount != 3 || !slices.Equal(info.Pages, want) || !info.HasCover ||
		info.Meta.Title != "Bande dessinée" || !slices.Equal(info.Meta.Authors, []string{"A", "B"}) || info.Meta.Date != "2019-03-04" {
		t.Fatalf("scanned PDF: %+v", info)
	}
	for i, pg := range pages {
		data, ext, err := Page(p, i)
		if err != nil || ext != ".jpg" || !bytes.Equal(data, pg.JPEG) {
			t.Fatalf("page %d: %v", i, err)
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			t.Fatalf("page %d unreadable: %v", i, err)
		}
	}
}

func TestTextPDF(t *testing.T) {
	p := write(t, "doc.pdf", testfixtures.TextPDF("Bonjour", map[string]string{"Title": "Été", "Author": "Autrice"}))
	info, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	// Cross-reference stream (PNG predictor) and object streams: both read.
	if info.Layout != domain.LayoutDocument || info.PageCount != 1 || info.HasCover || info.Meta.Title != "Été" ||
		!slices.Equal(info.Meta.Authors, []string{"Autrice"}) {
		t.Fatalf("text PDF: %+v", info)
	}
	if _, _, err := Page(p, 0); !errors.Is(err, ErrNoPage) {
		t.Errorf("page of a document: %v", err)
	}
	// A damaged PDF can still be read, rendered by the client.
	broken := write(t, "abime.pdf", []byte("%PDF-1.4\ncassé"))
	if info, err := Read(broken); err != nil || info.Layout != domain.LayoutDocument {
		t.Fatalf("damaged PDF: %+v %v", info, err)
	}
}

// Real books, if a folder is given in LATERNA_TEST_BOOKS (read only).
func TestRealBooks(t *testing.T) {
	dir := os.Getenv("LATERNA_TEST_BOOKS")
	if dir == "" {
		t.Skip("LATERNA_TEST_BOOKS is not set")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if _, ok := FormatOf(p); !ok {
			continue
		}
		info, err := Read(p)
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		t.Logf("%s: %s, %s, %d pages, rtl=%v, cover=%v, %+v", e.Name(), info.Format, info.Layout, info.PageCount, info.RightToLeft, info.HasCover, info.Meta)
		if info.HasCover {
			data, ext, err := Cover(p)
			if err != nil {
				t.Errorf("%s: cover: %v", e.Name(), err)
				continue
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
			t.Logf("  cover %s %d×%d (%v)", ext, cfg.Width, cfg.Height, err)
		}
	}
}
