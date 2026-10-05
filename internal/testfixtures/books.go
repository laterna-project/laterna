package testfixtures

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// Books.
//
// Written in Go, without FFmpeg: EPUB, CBZ and PDF are archives or text. Page images are flat
// colors, numbered by their hue.

var pagePalette = []color.RGBA{
	{R: 200, G: 80, B: 60, A: 255},
	{R: 60, G: 160, B: 90, A: 255},
	{R: 70, G: 90, B: 200, A: 255},
	{R: 220, G: 180, B: 50, A: 255},
	{R: 150, G: 70, B: 170, A: 255},
	{R: 50, G: 170, B: 180, A: 255},
	{R: 230, G: 120, B: 160, A: 255},
	{R: 120, G: 120, B: 120, A: 255},
}

// PageImage returns a synthetic w×h page whose hue depends on n.
func PageImage(w, h, n int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := pagePalette[n%len(pagePalette)]
	for y := range h {
		for x := range w {
			// A darker frame: the page has a visible border, like a comic page.
			if x < 8 || y < 8 || x >= w-8 || y >= h-8 {
				img.Set(x, y, color.RGBA{R: c.R / 3, G: c.G / 3, B: c.B / 3, A: 255})
			} else {
				img.Set(x, y, c)
			}
		}
	}
	return img
}

// JPEG encodes an image as JPEG.
func JPEG(img image.Image) []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	return b.Bytes()
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// ZipEntry is a file of a ZIP archive; Store stores it uncompressed.
type ZipEntry struct {
	Name  string
	Data  []byte
	Store bool
}

// Zip builds a ZIP archive, in the order of the entries.
func Zip(entries []ZipEntry) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range entries {
		method := zip.Deflate
		if e.Store {
			method = zip.Store
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: e.Name, Method: method})
		if err == nil {
			_, _ = f.Write(e.Data)
		}
	}
	_ = w.Close()
	return b.Bytes()
}

// EPUB builds a minimal EPUB 2: the given OPF (content.opf), one chapter and, if cover is not nil,
// its cover (cover.jpg).
func EPUB(opf string, cover []byte) []byte {
	entries := []ZipEntry{
		{Name: "mimetype", Data: []byte("application/epub+zip"), Store: true},
		{Name: "META-INF/container.xml", Data: []byte(`<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)},
		{Name: "OEBPS/content.opf", Data: []byte(opf)},
		{Name: "OEBPS/text/chapter1.xhtml", Data: []byte(`<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter 1</title></head>
<body><h1>Chapter 1</h1><p>Once upon a time there was a test book.</p></body></html>`)},
	}
	if cover != nil {
		entries = append(entries, ZipEntry{Name: "OEBPS/Images/cover.jpg", Data: cover})
	}
	return Zip(entries)
}

// PDFPage is a page of a scanned PDF: its JPEG image and its dimensions.
type PDFPage struct {
	JPEG          []byte
	Width, Height int
}

// ScannedPDF builds a PDF in which each page is a single JPEG image (a scanned comic), with a
// classic cross-reference table and an Info dictionary.
func ScannedPDF(pages []PDFPage, info map[string]string) []byte {
	var p pdfWriter
	p.b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// 1: catalog, 2: page tree, 3: Info; then page, content and image for each page.
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+3*i)
	}
	p.object(1, "<< /Type /Catalog /Pages 2 0 R >>")
	p.object(2, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), strings.Join(kids, " ")))
	p.object(3, infoDict(info))
	for i, pg := range pages {
		page, contents, img := 4+3*i, 5+3*i, 6+3*i
		// Page at 72 points per inch for an image at 96: same proportions.
		w, h := float64(pg.Width)*0.75, float64(pg.Height)*0.75
		p.object(page, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>", w, h, img, contents))
		p.stream(contents, "", fmt.Appendf(nil, "q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", w, h))
		p.stream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", pg.Width, pg.Height), pg.JPEG)
	}
	start := p.b.Len()
	n := 4 + 3*len(pages)
	fmt.Fprintf(&p.b, "xref\n0 %d\n0000000000 65535 f\r\n", n)
	for i := 1; i < n; i++ {
		fmt.Fprintf(&p.b, "%010d 00000 n\r\n", p.offsets[i])
	}
	fmt.Fprintf(&p.b, "trailer\n<< /Size %d /Root 1 0 R /Info 3 0 R >>\nstartxref\n%d\n%%%%EOF\n", n, start)
	return p.b.Bytes()
}

// TextPDF builds a one-page text PDF the PDF 1.5 way: objects stored in an object stream,
// cross-reference table as a compressed stream with a PNG predictor.
func TextPDF(text string, info map[string]string) []byte {
	var p pdfWriter
	p.b.WriteString("%PDF-1.5\n%\xe2\xe3\xcf\xd3\n")
	content := fmt.Appendf(nil, "BT /F1 24 Tf 72 700 Td (%s) Tj ET", text)
	p.stream(1, "", content)
	// Objects 3 to 7 go in object stream 2.
	objs := []string{
		"<< /Type /Catalog /Pages 4 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [5 0 R] >>",
		"<< /Type /Page /Parent 4 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 6 0 R >> >> /Contents 1 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		infoDict(info),
	}
	var header, body strings.Builder
	for i, o := range objs {
		fmt.Fprintf(&header, "%d %d ", 3+i, body.Len())
		body.WriteString(o + "\n")
	}
	first := header.Len()
	p.stream(2, fmt.Sprintf("/Type /ObjStm /N %d /First %d /Filter /FlateDecode", len(objs), first), deflate([]byte(header.String()+body.String())))
	// Cross-reference table: type (1 byte), offset or stream (4), index (2); 9 objects (0 to 8).
	xrefOffset := p.b.Len()
	rows := make([][]byte, 9)
	rows[0] = []byte{0, 0, 0, 0, 0, 0xff, 0xff}
	for num := 1; num <= 2; num++ {
		rows[num] = entry(1, uint32(p.offsets[num]), 0) //nolint:gosec // small test file
	}
	for i := range objs {
		rows[3+i] = entry(2, 2, uint16(i)) //nolint:gosec // small test file
	}
	rows[8] = entry(1, uint32(xrefOffset), 0) //nolint:gosec // small test file
	var raw []byte
	prev := make([]byte, 7)
	for _, r := range rows {
		up := make([]byte, 7)
		for i := range r {
			up[i] = r[i] - prev[i]
		}
		raw = append(append(raw, 2), up...) // PNG "Up" filter
		prev = r
	}
	p.stream(8, "/Type /XRef /Size 9 /W [1 4 2] /Root 3 0 R /Info 7 0 R /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 7 >>", deflate(raw))
	fmt.Fprintf(&p.b, "startxref\n%d\n%%%%EOF\n", xrefOffset)
	return p.b.Bytes()
}

func entry(kind byte, field2 uint32, field3 uint16) []byte {
	b := []byte{kind, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[1:5], field2)
	binary.BigEndian.PutUint16(b[5:7], field3)
	return b
}

func deflate(data []byte) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, _ = w.Write(data)
	_ = w.Close()
	return b.Bytes()
}

// infoDict writes an Info dictionary; non-ASCII strings as UTF-16 (hex).
func infoDict(info map[string]string) string {
	var b strings.Builder
	b.WriteString("<<")
	for _, k := range []string{"Title", "Author", "Subject", "CreationDate", "Producer"} {
		v, ok := info[k]
		if !ok {
			continue
		}
		ascii := true
		for _, r := range v {
			ascii = ascii && r < 128 && r != '(' && r != ')' && r != '\\'
		}
		if ascii {
			fmt.Fprintf(&b, " /%s (%s)", k, v)
			continue
		}
		fmt.Fprintf(&b, " /%s <FEFF", k)
		for _, r := range v {
			fmt.Fprintf(&b, "%04X", r)
		}
		b.WriteString(">")
	}
	b.WriteString(" >>")
	return b.String()
}

type pdfWriter struct {
	b       bytes.Buffer
	offsets map[int]int
}

func (p *pdfWriter) object(num int, body string) {
	if p.offsets == nil {
		p.offsets = map[int]int{}
	}
	p.offsets[num] = p.b.Len()
	fmt.Fprintf(&p.b, "%d 0 obj\n%s\nendobj\n", num, body)
}

func (p *pdfWriter) stream(num int, dict string, data []byte) {
	if p.offsets == nil {
		p.offsets = map[int]int{}
	}
	p.offsets[num] = p.b.Len()
	fmt.Fprintf(&p.b, "%d 0 obj\n<< %s /Length %d >>\nstream\r\n", num, dict, len(data))
	p.b.Write(data)
	p.b.WriteString("\nendstream\nendobj\n")
}

// books writes the book library.
func (g *generator) books(context.Context) error {
	root := filepath.Join(g.root, "Books")
	cover := JPEG(PageImage(300, 450, 1))

	// Novel prepared by Calibre: series, author, HTML description, date at midnight Paris time.
	novel := EPUB(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="uuid_id">
  <metadata xmlns:opf="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:calibre="http://calibre.kovidgoyal.net/2009/metadata">
    <dc:title>The Voyage, Book 2: The Crossing</dc:title>
    <dc:creator opf:role="aut" opf:file-as="Test, Author">Author Test</dc:creator>
    <dc:contributor opf:role="bkp">calibre (6.9.0)</dc:contributor>
    <dc:description>&lt;p&gt;A &lt;b&gt;test&lt;/b&gt; novel.&lt;/p&gt;&lt;p&gt;Second paragraph.&lt;/p&gt;</dc:description>
    <dc:publisher>Test Press</dc:publisher>
    <dc:date>2023-11-30T23:00:00+00:00</dc:date>
    <dc:language>en</dc:language>
    <dc:identifier opf:scheme="ISBN">978-2-1234-5680-3</dc:identifier>
    <dc:subject>Adventure</dc:subject>
    <dc:subject>Young Adult</dc:subject>
    <meta name="calibre:series" content="The Voyage"/>
    <meta name="calibre:series_index" content="2.0"/>
    <meta name="calibre:title_sort" content="Voyage, Book 2, The"/>
    <meta name="cover" content="cover"/>
  </metadata>
  <manifest>
    <item id="cover" href="Images/cover.jpg" media-type="image/jpeg"/>
    <item id="c1" href="text/chapter1.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="c1"/></spine>
</package>`, cover)
	if err := g.bytes(filepath.Join(root, "Novels", "The Voyage V2 - Author Test.epub"), novel); err != nil {
		return err
	}

	// Calibre library: the book alone in its folder, with metadata.opf and cover.jpg next to it.
	// The EPUB itself says next to nothing.
	calibre := filepath.Join(root, "Calibre", "Author Test", "Solo Novel (12)")
	bare := EPUB(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Untitled</dc:title></metadata>
  <manifest><item id="c1" href="text/chapter1.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="c1"/></spine>
</package>`, nil)
	if err := g.bytes(filepath.Join(calibre, "Solo Novel - Author Test.epub"), bare); err != nil {
		return err
	}
	if err := g.text(filepath.Join(calibre, "metadata.opf"), `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:opf="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Solo Novel</dc:title>
    <dc:creator opf:role="aut">Author Test</dc:creator>
    <dc:description>A novel filed by Calibre.</dc:description>
    <dc:date>2021-03-04</dc:date>
    <dc:language>en</dc:language>
  </metadata>
</package>`); err != nil {
		return err
	}
	if err := g.bytes(filepath.Join(calibre, "cover.jpg"), JPEG(PageImage(300, 450, 2))); err != nil {
		return err
	}

	// Manga with ComicInfo.xml: right to left, a double page, cover named explicitly.
	manga := filepath.Join(root, "Manga", "Manga Test")
	ci := `<?xml version="1.0" encoding="utf-8"?>
<ComicInfo xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <Title>The Beginning</Title><Series>Manga Test</Series><Number>1</Number>
  <Summary>A test manga.</Summary><Year>2020</Year><Month>5</Month><Day>12</Day>
  <Writer>Writer Test</Writer><Penciller>Penciller Test</Penciller>
  <Publisher>Test Press</Publisher><Genre>Action, Adventure</Genre><LanguageISO>en</LanguageISO>
  <Manga>YesAndRightToLeft</Manga>
  <Pages><Page Image="1" Type="FrontCover"/></Pages>
</ComicInfo>`
	volume1 := Zip([]ZipEntry{
		{Name: "ComicInfo.xml", Data: []byte(ci)},
		{Name: "page-00.png", Data: pngBytes(PageImage(400, 600, 10))}, // flyleaf
		{Name: "page-01.png", Data: pngBytes(PageImage(400, 600, 11))}, // cover
		{Name: "page-02.png", Data: pngBytes(PageImage(800, 600, 12))}, // double page
		{Name: "__MACOSX/._page-01.png", Data: []byte("x")},
	})
	if err := g.bytes(filepath.Join(manga, "Manga Test - Volume #01 - [V1].cbz"), volume1); err != nil {
		return err
	}
	// No ComicInfo, release-style name, pages sorted by chapter (natural order).
	var pages []ZipEntry
	for _, name := range []string{"Ch 10/10.jpg", "Ch 10/2.jpg", "Ch 9/1.jpg"} {
		pages = append(pages, ZipEntry{Name: name, Data: JPEG(PageImage(400, 600, len(pages)+20))})
	}
	if err := g.bytes(filepath.Join(manga, "Manga.Test.V02.EN.[CBZ]-TEAM.cbz"), Zip(pages)); err != nil {
		return err
	}

	// Scanned comic as a PDF, without metadata.
	var scans []PDFPage
	for i := range 3 {
		scans = append(scans, PDFPage{JPEG: JPEG(PageImage(400, 560, 30+i)), Width: 400, Height: 560})
	}
	if err := g.bytes(filepath.Join(root, "Big.Comic.Book.1.2019.EN.[PDF]-NOTAG.pdf"), ScannedPDF(scans, map[string]string{"Producer": "Test"})); err != nil {
		return err
	}
	// Text PDF document, rendered by the client.
	return g.bytes(filepath.Join(root, "Field Guide.pdf"), TextPDF("Hello", map[string]string{
		"Title": "Guide to Testing", "Author": "Author Test", "CreationDate": "D:20220115093000+01'00'",
	}))
}

// bytes writes a file generated in Go (temporary name, then renamed).
func (g *generator) bytes(path string, data []byte) error {
	if g.exists(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	partial := filepath.Join(filepath.Dir(path), fmt.Sprintf(".partial-%d-%s", os.Getpid(), filepath.Base(path)))
	if err := os.WriteFile(partial, data, 0o600); err != nil {
		return err
	}
	g.made++
	if g.log != nil {
		g.log(path)
	}
	return os.Rename(partial, path)
}
