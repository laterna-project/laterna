package books

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/metadata"
)

// A minimal PDF reader that renders nothing: cross-reference table (classic or as a stream, PDF
// 1.5), object streams, page tree, Info dictionary. Enough to count pages, recognize a scanned PDF
// (each page is a single JPEG image) and pull those images out untouched.

type (
	pdfName    string
	pdfString  []byte
	pdfKeyword string
	pdfRef     struct{ num, gen int }
	pdfDict    map[pdfName]any
	pdfArray   []any
	// pdfStream is the dictionary of a stream and the absolute position of its data in the file.
	pdfStream struct {
		dict   pdfDict
		offset int64
	}
)

// xrefEntry is an object stored at offset (kind 1), or the index-th object of an object stream
// (kind 2).
type xrefEntry struct {
	kind   byte
	offset int64
	stream int
	index  int
}

type pdfDoc struct {
	ra      io.ReaderAt
	size    int64
	xref    map[int]xrefEntry
	trailer pdfDict
	objStms map[int]*objStm
}

type objStm struct {
	data    []byte
	offsets map[int]int // object number -> position in data
}

// Limits against a malicious PDF.
const (
	maxXrefSections = 64
	maxResolveDepth = 32
	maxTreeDepth    = 64
)

var errPDF = errors.New("invalid PDF structure")

func openPDF(f *os.File) (*pdfDoc, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	d := &pdfDoc{ra: f, size: st.Size(), xref: map[int]xrefEntry{}, objStms: map[int]*objStm{}}
	tail := min(d.size, 2048)
	buf := make([]byte, tail)
	if _, err := f.ReadAt(buf, d.size-tail); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	i := bytes.LastIndex(buf, []byte("startxref"))
	if i < 0 {
		return nil, fmt.Errorf("%w: missing startxref", errPDF)
	}
	l := newLexer(d.ra, d.size-tail+int64(i)+int64(len("startxref")), d.size)
	tok, err := l.token()
	off, ok := tok.(int64)
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: unreadable startxref", errPDF)
	}
	if err := d.readXref(off, map[int64]bool{}); err != nil {
		return nil, err
	}
	if d.trailer == nil {
		return nil, fmt.Errorf("%w: missing trailer", errPDF)
	}
	return d, nil
}

// readXref reads one section of the cross-reference table (and the previous ones). A newer section
// wins over older ones.
func (d *pdfDoc) readXref(off int64, seen map[int64]bool) error {
	if seen[off] || len(seen) >= maxXrefSections || off < 0 || off >= d.size {
		return nil
	}
	seen[off] = true
	l := newLexer(d.ra, off, d.size)
	tok, err := l.token()
	if err != nil {
		return err
	}
	var trailer pdfDict
	if tok == pdfKeyword("xref") {
		if trailer, err = d.classicXref(l); err != nil {
			return err
		}
		if stm, ok := trailer["XRefStm"].(int64); ok {
			if err := d.readXref(stm, seen); err != nil {
				return err
			}
		}
	} else {
		obj, err := d.readObjectAt(off)
		if err != nil {
			return err
		}
		s, ok := obj.(*pdfStream)
		if !ok || s.dict["Type"] != pdfName("XRef") {
			return fmt.Errorf("%w: object table not found", errPDF)
		}
		if err := d.streamXref(s); err != nil {
			return err
		}
		trailer = s.dict
	}
	if d.trailer == nil {
		d.trailer = trailer
	}
	if prev, ok := trailer["Prev"].(int64); ok {
		return d.readXref(prev, seen)
	}
	return nil
}

func (d *pdfDoc) classicXref(l *lexer) (pdfDict, error) {
	for {
		tok, err := l.token()
		if err != nil {
			return nil, err
		}
		if tok == pdfKeyword("trailer") {
			obj, err := l.object()
			if err != nil {
				return nil, err
			}
			dict, ok := obj.(pdfDict)
			if !ok {
				return nil, fmt.Errorf("%w: trailer", errPDF)
			}
			return dict, nil
		}
		start, ok1 := tok.(int64)
		countTok, err := l.token()
		count, ok2 := countTok.(int64)
		if err != nil || !ok1 || !ok2 || count < 0 || count > 10_000_000 {
			return nil, fmt.Errorf("%w: object table", errPDF)
		}
		for i := range count {
			offTok, _ := l.token()
			_, _ = l.token() // generation
			kind, err := l.token()
			if err != nil {
				return nil, err
			}
			num := int(start + i)
			off, ok := offTok.(int64)
			if _, known := d.xref[num]; !known && ok && kind == pdfKeyword("n") {
				d.xref[num] = xrefEntry{kind: 1, offset: off}
			}
		}
	}
}

func (d *pdfDoc) streamXref(s *pdfStream) error {
	data, err := d.streamData(s)
	if err != nil {
		return err
	}
	w, ok := s.dict["W"].(pdfArray)
	if !ok || len(w) != 3 {
		return fmt.Errorf("%w: /W", errPDF)
	}
	var widths [3]int
	row := 0
	for i, v := range w {
		n, ok := v.(int64)
		if !ok || n < 0 || n > 8 {
			return fmt.Errorf("%w: /W", errPDF)
		}
		widths[i] = int(n)
		row += int(n)
	}
	size, _ := s.dict["Size"].(int64)
	index := pdfArray{int64(0), size}
	if idx, ok := s.dict["Index"].(pdfArray); ok {
		index = idx
	}
	field := func(b []byte) int64 {
		var v int64
		for _, c := range b {
			v = v<<8 | int64(c)
		}
		return v
	}
	pos := 0
	for i := 0; i+1 < len(index); i += 2 {
		start, _ := index[i].(int64)
		count, _ := index[i+1].(int64)
		for j := range count {
			if pos+row > len(data) {
				return nil
			}
			b := data[pos : pos+row]
			pos += row
			kind := int64(1)
			if widths[0] > 0 {
				kind = field(b[:widths[0]])
			}
			f2, f3 := field(b[widths[0]:widths[0]+widths[1]]), field(b[widths[0]+widths[1]:])
			num := int(start + j)
			if _, known := d.xref[num]; known {
				continue
			}
			switch kind {
			case 1:
				d.xref[num] = xrefEntry{kind: 1, offset: f2}
			case 2:
				d.xref[num] = xrefEntry{kind: 2, stream: int(f2), index: int(f3)}
			}
		}
	}
	return nil
}

// readObjectAt reads the indirect object stored at off ("12 0 obj ... endobj").
func (d *pdfDoc) readObjectAt(off int64) (any, error) {
	l := newLexer(d.ra, off, d.size)
	for range 3 { // number, generation, "obj"
		if _, err := l.token(); err != nil {
			return nil, err
		}
	}
	obj, err := l.object()
	if err != nil {
		return nil, err
	}
	dict, ok := obj.(pdfDict)
	if !ok {
		return obj, nil
	}
	tok, err := l.token()
	if err != nil || tok != pdfKeyword("stream") {
		return dict, nil //nolint:nilerr // a dictionary without a stream ends here
	}
	// The data starts after the end of line that follows "stream".
	if b, ok := l.next(); ok && b == '\r' {
		if b, ok := l.next(); ok && b != '\n' {
			l.back()
		}
	} else if ok && b != '\n' {
		l.back()
	}
	return &pdfStream{dict: dict, offset: l.offset()}, nil
}

// get returns object num, or nil if it does not exist.
func (d *pdfDoc) get(num int) (any, error) {
	e, ok := d.xref[num]
	if !ok {
		return nil, nil
	}
	if e.kind == 1 {
		return d.readObjectAt(e.offset)
	}
	stm, err := d.objStm(e.stream)
	if err != nil {
		return nil, err
	}
	pos, ok := stm.offsets[num]
	if !ok || pos >= len(stm.data) {
		return nil, nil
	}
	return newBytesLexer(stm.data[pos:]).object()
}

func (d *pdfDoc) objStm(num int) (*objStm, error) {
	if s, ok := d.objStms[num]; ok {
		return s, nil
	}
	e, ok := d.xref[num]
	if !ok || e.kind != 1 {
		return nil, fmt.Errorf("%w: object stream %d", errPDF, num)
	}
	obj, err := d.readObjectAt(e.offset)
	if err != nil {
		return nil, err
	}
	s, ok := obj.(*pdfStream)
	if !ok {
		return nil, fmt.Errorf("%w: object stream %d", errPDF, num)
	}
	data, err := d.streamData(s)
	if err != nil {
		return nil, err
	}
	n, _ := s.dict["N"].(int64)
	first, _ := s.dict["First"].(int64)
	if first < 0 || first > int64(len(data)) || n < 0 || n > 1_000_000 {
		return nil, fmt.Errorf("%w: object stream %d", errPDF, num)
	}
	out := &objStm{data: data, offsets: map[int]int{}}
	l := newBytesLexer(data[:first])
	for range n {
		// An unreadable item is nil: the header ends there.
		numTok, _ := l.token()
		offTok, _ := l.token()
		objNum, ok1 := numTok.(int64)
		off, ok2 := offTok.(int64)
		if !ok1 || !ok2 {
			break
		}
		out.offsets[int(objNum)] = int(first + off)
	}
	d.objStms[num] = out
	return out, nil
}

// resolve follows references down to a direct object.
func (d *pdfDoc) resolve(obj any) (any, error) {
	for range maxResolveDepth {
		ref, ok := obj.(pdfRef)
		if !ok {
			return obj, nil
		}
		var err error
		if obj, err = d.get(ref.num); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: reference loop", errPDF)
}

func (d *pdfDoc) dict(obj any) pdfDict {
	obj, _ = d.resolve(obj)
	switch v := obj.(type) {
	case pdfDict:
		return v
	case *pdfStream:
		if v != nil {
			return v.dict
		}
	}
	return nil
}

func (d *pdfDoc) int(obj any) (int64, bool) {
	obj, _ = d.resolve(obj)
	switch v := obj.(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}
	return 0, false
}

// rawStream returns the data of a stream as it is in the file.
func (d *pdfDoc) rawStream(s *pdfStream) ([]byte, error) {
	n, ok := d.int(s.dict["Length"])
	if !ok || n < 0 || n > maxEntry || s.offset+n > d.size {
		return nil, fmt.Errorf("%w: stream length", errPDF)
	}
	buf := make([]byte, n)
	if _, err := d.ra.ReadAt(buf, s.offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf, nil
}

// streamData returns the decoded data of a stream (no filter, or FlateDecode with or without a PNG
// predictor).
func (d *pdfDoc) streamData(s *pdfStream) ([]byte, error) {
	raw, err := d.rawStream(s)
	if err != nil {
		return nil, err
	}
	filters := filtersOf(s.dict)
	switch {
	case len(filters) == 0:
		return raw, nil
	case len(filters) > 1 || filters[0] != "FlateDecode":
		return nil, fmt.Errorf("unsupported filter %v", filters)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(zr, maxEntry))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	parms := d.dict(s.dict["DecodeParms"])
	if arr, ok := s.dict["DecodeParms"].(pdfArray); ok && len(arr) > 0 {
		parms = d.dict(arr[0])
	}
	if pred, _ := d.int(parms["Predictor"]); pred >= 10 {
		columns, ok := d.int(parms["Columns"])
		if !ok {
			columns = 1
		}
		colors, ok := d.int(parms["Colors"])
		if !ok {
			colors = 1
		}
		bpc, ok := d.int(parms["BitsPerComponent"])
		if !ok {
			bpc = 8
		}
		return pngUnfilter(data, int(columns*colors*bpc+7)/8, int(max(1, colors*bpc/8)))
	}
	return data, nil
}

func filtersOf(dict pdfDict) []pdfName {
	switch f := dict["Filter"].(type) {
	case pdfName:
		return []pdfName{f}
	case pdfArray:
		var out []pdfName
		for _, v := range f {
			if n, ok := v.(pdfName); ok {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

// pngUnfilter undoes the PNG predictor (one filter byte per row).
func pngUnfilter(data []byte, rowLen, bpp int) ([]byte, error) {
	if rowLen <= 0 || rowLen > 1<<20 {
		return nil, fmt.Errorf("%w: predictor", errPDF)
	}
	out := make([]byte, 0, len(data))
	prev := make([]byte, rowLen)
	for pos := 0; pos+1+rowLen <= len(data); pos += 1 + rowLen {
		filter, row := data[pos], append([]byte(nil), data[pos+1:pos+1+rowLen]...)
		for i := range row {
			var left, up, upLeft byte
			if i >= bpp {
				left, upLeft = row[i-bpp], prev[i-bpp]
			}
			up = prev[i]
			switch filter {
			case 1:
				row[i] += left
			case 2:
				row[i] += up
			case 3:
				row[i] += byte((uint16(left) + uint16(up)) / 2) //nolint:gosec // average of two bytes, <= 255
			case 4:
				row[i] += paeth(left, up, upLeft)
			}
		}
		out = append(out, row...)
		prev = row
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := absInt(p-int(a)), absInt(p-int(b)), absInt(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Pages.

// pageNode is a page and what it inherits from its parents.
type pageNode struct {
	resources pdfDict
	mediaBox  pdfArray
	rotate    int64
}

// pages walks the page tree in order.
func (d *pdfDoc) pages() ([]pageNode, error) {
	root := d.dict(d.trailer["Root"])
	if root == nil {
		return nil, fmt.Errorf("%w: missing catalog", errPDF)
	}
	var out []pageNode
	seen := map[pdfRef]bool{}
	var walk func(node any, inherited pageNode, depth int) error
	walk = func(node any, inherited pageNode, depth int) error {
		if ref, ok := node.(pdfRef); ok {
			if seen[ref] {
				return nil
			}
			seen[ref] = true
		}
		dict := d.dict(node)
		if dict == nil || depth > maxTreeDepth || len(out) >= maxPages {
			return nil
		}
		if r := d.dict(dict["Resources"]); r != nil {
			inherited.resources = r
		}
		if mb, err := d.resolve(dict["MediaBox"]); err == nil {
			if arr, ok := mb.(pdfArray); ok && len(arr) == 4 {
				inherited.mediaBox = arr
			}
		}
		if r, ok := d.int(dict["Rotate"]); ok {
			inherited.rotate = r
		}
		kids, err := d.resolve(dict["Kids"])
		if err != nil {
			return err
		}
		if arr, ok := kids.(pdfArray); ok && dict["Type"] != pdfName("Page") {
			for _, kid := range arr {
				if err := walk(kid, inherited, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		out = append(out, inherited)
		return nil
	}
	return out, walk(root["Pages"], pageNode{}, 0)
}

// pageCount reads the page count announced by the tree.
func (d *pdfDoc) pageCount() int {
	root := d.dict(d.trailer["Root"])
	n, _ := d.int(d.dict(root["Pages"])["Count"])
	return int(min(max(n, 0), maxPages))
}

// pageImage returns the JPEG image of a scanned page: its only image, covering the whole page. ok
// is false for any other page.
func (d *pdfDoc) pageImage(p pageNode) (img *pdfStream, size domain.PageSize, ok bool) {
	if p.rotate%360 != 0 || len(p.mediaBox) != 4 {
		return nil, size, false
	}
	xobjects := d.dict(p.resources["XObject"])
	if len(xobjects) != 1 {
		return nil, size, false
	}
	for _, v := range xobjects {
		obj, err := d.resolve(v)
		s, isStream := obj.(*pdfStream)
		if err != nil || !isStream || s.dict["Subtype"] != pdfName("Image") || s.dict["SMask"] != nil {
			return nil, size, false
		}
		if f := filtersOf(s.dict); len(f) != 1 || f[0] != "DCTDecode" {
			return nil, size, false
		}
		w, ok1 := d.int(s.dict["Width"])
		h, ok2 := d.int(s.dict["Height"])
		if !ok1 || !ok2 || w <= 0 || h <= 0 || w*h > maxPagePixels {
			return nil, size, false
		}
		img, size = s, domain.PageSize{Width: int(w), Height: int(h)}
	}
	var box [4]float64
	for i, v := range p.mediaBox {
		switch n := v.(type) {
		case int64:
			box[i] = float64(n)
		case float64:
			box[i] = n
		}
	}
	pw, ph := math.Abs(box[2]-box[0]), math.Abs(box[3]-box[1])
	if pw == 0 || ph == 0 {
		return nil, size, false
	}
	pageRatio, imageRatio := pw/ph, float64(size.Width)/float64(size.Height)
	return img, size, math.Abs(pageRatio-imageRatio)/pageRatio < 0.05
}

func readPDF(p string) (Info, error) {
	f, err := os.Open(p) //nolint:gosec // library file, path taken from the database
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = f.Close() }()
	// A PDF this reader does not understand can still be read, rendered by the client.
	info := Info{Layout: domain.LayoutDocument}
	d, err := openPDF(f)
	if err != nil {
		return info, nil
	}
	info.PageCount = d.pageCount()
	encrypted := d.trailer["Encrypt"] != nil
	if !encrypted {
		info.Meta = metadata.PDFMeta(d.infoStrings())
	}
	pages, err := d.pages()
	if err != nil || encrypted || len(pages) == 0 {
		return info, nil
	}
	sizes := make([]domain.PageSize, 0, len(pages))
	for _, pg := range pages {
		_, size, ok := d.pageImage(pg)
		if !ok {
			return info, nil
		}
		sizes = append(sizes, size)
	}
	info.Layout, info.PageCount, info.Pages, info.HasCover = domain.LayoutImages, len(sizes), sizes, true
	return info, nil
}

func pdfPage(p string, n int) ([]byte, string, error) {
	f, err := os.Open(p) //nolint:gosec // library file, path taken from the database
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	d, err := openPDF(f)
	if err != nil {
		return nil, "", err
	}
	pages, err := d.pages()
	if err != nil {
		return nil, "", err
	}
	if n < 0 || n >= len(pages) {
		return nil, "", ErrNoPage
	}
	img, _, ok := d.pageImage(pages[n])
	if !ok || img == nil {
		return nil, "", ErrNoPage
	}
	data, err := d.rawStream(img)
	if err != nil {
		return nil, "", err
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, "", fmt.Errorf("%w: page %d is not a JPEG", errPDF, n)
	}
	return data, ".jpg", nil
}

// infoStrings reads the text entries of the Info dictionary.
func (d *pdfDoc) infoStrings() map[string]string {
	out := map[string]string{}
	for k, v := range d.dict(d.trailer["Info"]) {
		obj, err := d.resolve(v)
		if s, ok := obj.(pdfString); ok && err == nil {
			out[string(k)] = pdfText(s)
		}
	}
	return out
}

// pdfText decodes a text string: UTF-16 (with a byte order mark), UTF-8, otherwise Latin-1 (close
// to PDFDocEncoding).
func pdfText(s pdfString) string {
	switch {
	case len(s) >= 2 && s[0] == 0xFE && s[1] == 0xFF:
		u := make([]uint16, 0, len(s)/2)
		for i := 2; i+1 < len(s); i += 2 {
			u = append(u, uint16(s[i])<<8|uint16(s[i+1]))
		}
		return string(utf16.Decode(u))
	case len(s) >= 3 && s[0] == 0xEF && s[1] == 0xBB && s[2] == 0xBF:
		return string(s[3:])
	case utf8.Valid(s):
		return string(s)
	}
	r := make([]rune, len(s))
	for i, b := range s {
		r[i] = rune(b)
	}
	return string(r)
}

// Lexer.

// lexer reads PDF tokens from a position in a file (or a buffer).
type lexer struct {
	ra     io.ReaderAt
	end    int64
	base   int64 // position of buf[0]
	buf    []byte
	i      int
	peeked []any
}

func newLexer(ra io.ReaderAt, off, end int64) *lexer {
	return &lexer{ra: ra, end: end, base: off}
}

func newBytesLexer(b []byte) *lexer {
	return &lexer{ra: bytes.NewReader(b), end: int64(len(b))}
}

func (l *lexer) offset() int64 { return l.base + int64(l.i) }

func (l *lexer) next() (byte, bool) {
	if l.i >= len(l.buf) {
		pos := l.offset()
		if pos >= l.end {
			return 0, false
		}
		n := min(4096, l.end-pos)
		buf := make([]byte, n)
		m, err := l.ra.ReadAt(buf, pos)
		if m == 0 && err != nil {
			return 0, false
		}
		l.base, l.buf, l.i = pos, buf[:m], 0
	}
	b := l.buf[l.i]
	l.i++
	return b, true
}

// back steps back one byte (always possible right after next).
func (l *lexer) back() { l.i-- }

func isSpace(b byte) bool {
	return b == 0 || b == '\t' || b == '\n' || b == '\f' || b == '\r' || b == ' '
}

func isDelim(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// token reads the next token: int64, float64, pdfName, pdfString or pdfKeyword (keywords plus "<<",
// ">>", "[", "]").
func (l *lexer) token() (any, error) {
	if n := len(l.peeked); n > 0 {
		t := l.peeked[n-1]
		l.peeked = l.peeked[:n-1]
		return t, nil
	}
	var b byte
	for {
		c, ok := l.next()
		if !ok {
			return nil, io.ErrUnexpectedEOF
		}
		if c == '%' {
			for {
				c, ok := l.next()
				if !ok || c == '\n' || c == '\r' {
					break
				}
			}
			continue
		}
		if !isSpace(c) {
			b = c
			break
		}
	}
	switch b {
	case '[', ']':
		return pdfKeyword(string(b)), nil
	case '<':
		c, ok := l.next()
		if ok && c == '<' {
			return pdfKeyword("<<"), nil
		}
		if ok {
			l.back()
		}
		return l.hexString()
	case '>':
		c, ok := l.next()
		if ok && c == '>' {
			return pdfKeyword(">>"), nil
		}
		if ok {
			l.back()
		}
		return nil, fmt.Errorf("%w: stray \">\"", errPDF)
	case '(':
		return l.literalString()
	case '/':
		return l.name(), nil
	}
	word := []byte{b}
	for {
		c, ok := l.next()
		if !ok {
			break
		}
		if isSpace(c) || isDelim(c) {
			l.back()
			break
		}
		word = append(word, c)
	}
	s := string(word)
	if (b >= '0' && b <= '9') || b == '-' || b == '+' || b == '.' {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, nil
		}
	}
	return pdfKeyword(s), nil
}

func (l *lexer) name() pdfName {
	var out []byte
	for {
		c, ok := l.next()
		if !ok {
			break
		}
		if isSpace(c) || isDelim(c) {
			l.back()
			break
		}
		if c == '#' {
			h1, ok1 := l.next()
			h2, ok2 := l.next()
			if v, ok := unhex(h1, h2); ok && ok1 && ok2 {
				out = append(out, v)
				continue
			}
		}
		out = append(out, c)
	}
	return pdfName(out)
}

func (l *lexer) literalString() (pdfString, error) {
	var out []byte
	depth := 1
	for {
		c, ok := l.next()
		if !ok {
			return nil, io.ErrUnexpectedEOF
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return out, nil
			}
		case '\\':
			e, ok := l.next()
			if !ok {
				return nil, io.ErrUnexpectedEOF
			}
			switch e {
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case '\r':
				if n, ok := l.next(); ok && n != '\n' {
					l.back()
				}
				continue
			case '\n':
				continue
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for range 2 {
						d, ok := l.next()
						if !ok || d < '0' || d > '7' {
							if ok {
								l.back()
							}
							break
						}
						v = v*8 + int(d-'0')
					}
					c = byte(v)
				} else {
					c = e
				}
			}
		}
		out = append(out, c)
	}
}

func (l *lexer) hexString() (pdfString, error) {
	var digits []byte
	for {
		c, ok := l.next()
		if !ok {
			return nil, io.ErrUnexpectedEOF
		}
		if c == '>' {
			break
		}
		if !isSpace(c) {
			digits = append(digits, c)
		}
	}
	if len(digits)%2 == 1 {
		digits = append(digits, '0')
	}
	out := make([]byte, 0, len(digits)/2)
	for i := 0; i < len(digits); i += 2 {
		v, ok := unhex(digits[i], digits[i+1])
		if !ok {
			return nil, fmt.Errorf("%w: hexadecimal string", errPDF)
		}
		out = append(out, v)
	}
	return out, nil
}

// object reads a whole object: dictionary, array, reference ("12 0 R") or plain value.
func (l *lexer) object() (any, error) {
	return l.objectDepth(0)
}

func (l *lexer) objectDepth(depth int) (any, error) {
	if depth > maxTreeDepth {
		return nil, fmt.Errorf("%w: objects nested too deep", errPDF)
	}
	tok, err := l.token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case pdfKeyword:
		switch t {
		case "<<":
			dict := pdfDict{}
			for {
				key, err := l.token()
				if err != nil {
					return nil, err
				}
				if key == pdfKeyword(">>") {
					return dict, nil
				}
				name, ok := key.(pdfName)
				if !ok {
					return nil, fmt.Errorf("%w: dictionary key", errPDF)
				}
				v, err := l.objectDepth(depth + 1)
				if err != nil {
					return nil, err
				}
				dict[name] = v
			}
		case "[":
			var arr pdfArray
			for {
				tok, err := l.token()
				if err != nil {
					return nil, err
				}
				if tok == pdfKeyword("]") {
					return arr, nil
				}
				l.peeked = append(l.peeked, tok)
				v, err := l.objectDepth(depth + 1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		return t, nil
	case int64:
		// "12 0 R": a reference.
		gen, err := l.token()
		if err != nil {
			return t, nil //nolint:nilerr // data ends after a number
		}
		if g, ok := gen.(int64); ok {
			r, err := l.token()
			if err == nil && r == pdfKeyword("R") {
				return pdfRef{num: int(t), gen: int(g)}, nil
			}
			if err == nil {
				l.peeked = append(l.peeked, r)
			}
		}
		l.peeked = append(l.peeked, gen)
		return t, nil
	}
	return tok, nil
}
