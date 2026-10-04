package books

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/metadata"
	"github.com/laterna-project/laterna/internal/naming"
)

// EPUB.

// container.xml says where the OPF is.
type epubContainer struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

// epubOPF opens an EPUB and reads its OPF. opfPath is needed to resolve the paths it gives.
func epubOPF(zr *zip.Reader) (opf metadata.OPF, opfPath string, err error) {
	raw, err := readEntry(zr, "META-INF/container.xml")
	if err != nil {
		return metadata.OPF{}, "", fmt.Errorf("container.xml: %w", err)
	}
	var c epubContainer
	if err := xml.Unmarshal(raw, &c); err != nil {
		return metadata.OPF{}, "", fmt.Errorf("container.xml: %w", err)
	}
	if len(c.Rootfiles) == 0 {
		return metadata.OPF{}, "", errors.New("container.xml names no OPF")
	}
	opfPath = c.Rootfiles[0].FullPath
	raw, err = readEntry(zr, opfPath)
	if err != nil {
		return metadata.OPF{}, "", fmt.Errorf("OPF: %w", err)
	}
	opf, err = metadata.ParseOPF(bytes.NewReader(raw))
	if err != nil {
		return metadata.OPF{}, "", fmt.Errorf("OPF: %w", err)
	}
	return opf, opfPath, nil
}

func readEPUB(p string) (Info, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = zr.Close() }()
	opf, opfPath, err := epubOPF(&zr.Reader)
	if err != nil {
		return Info{}, err
	}
	info := Info{Layout: domain.LayoutReflowable, RightToLeft: opf.Meta.RightToLeft, Meta: opf.Meta}
	if opf.CoverHref != "" {
		info.HasCover = findEntry(&zr.Reader, resolve(opfPath, opf.CoverHref)) != nil
	}
	return info, nil
}

func epubCover(p string) ([]byte, string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = zr.Close() }()
	opf, opfPath, err := epubOPF(&zr.Reader)
	if err != nil {
		return nil, "", err
	}
	name := resolve(opfPath, opf.CoverHref)
	ext := imageExt(name)
	if opf.CoverHref == "" || ext == "" {
		return nil, "", errors.New("EPUB without a cover")
	}
	data, err := readEntry(&zr.Reader, name)
	return data, ext, err
}

// resolve resolves a path relative to the OPF ("../Images/cover.jpg").
func resolve(opfPath, href string) string {
	href, _, _ = strings.Cut(href, "#")
	return path.Clean(path.Join(path.Dir(opfPath), unescapePath(href)))
}

// unescapePath decodes the "%20" found in the paths of an OPF.
func unescapePath(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, ok := unhex(s[i+1], s[i+2]); ok {
				b.WriteByte(v)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(a, b byte) (byte, bool) {
	h := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10, true
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10, true
		}
		return 0, false
	}
	x, ok1 := h(a)
	y, ok2 := h(b)
	return x<<4 | y, ok1 && ok2
}

// CBZ.

// cbzPages returns the images of an archive in reading order (natural order of the paths: "2.jpg"
// before "10.jpg", chapter folders included) and its ComicInfo.xml if it has one.
func cbzPages(zr *zip.Reader) (pages []*zip.File, comicInfo *zip.File) {
	for _, f := range zr.File {
		name := f.Name
		if f.FileInfo().IsDir() || hiddenEntry(name) {
			continue
		}
		if strings.EqualFold(path.Base(name), "ComicInfo.xml") {
			if comicInfo == nil || !strings.Contains(name, "/") {
				comicInfo = f
			}
			continue
		}
		if imageExt(name) != "" {
			pages = append(pages, f)
		}
	}
	slices.SortStableFunc(pages, func(a, b *zip.File) int { return naming.NaturalCompare(a.Name, b.Name) })
	if len(pages) > maxPages {
		pages = pages[:maxPages]
	}
	return pages, comicInfo
}

// hiddenEntry reports files added by macOS or Windows ("__MACOSX/", ".DS_Store", "Thumbs.db").
func hiddenEntry(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") || strings.EqualFold(part, "__MACOSX") || strings.EqualFold(part, "Thumbs.db") {
			return true
		}
	}
	return false
}

func readCBZ(p string) (Info, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = zr.Close() }()
	pages, ci := cbzPages(&zr.Reader)
	if len(pages) == 0 {
		return Info{}, errors.New("no image in the archive")
	}
	info := Info{Layout: domain.LayoutImages, PageCount: len(pages), HasCover: true}
	for _, f := range pages {
		size, err := entrySize(f)
		if err != nil {
			return Info{}, fmt.Errorf("page %s: %w", f.Name, err)
		}
		info.Pages = append(info.Pages, size)
	}
	if ci != nil {
		if c, err := comicInfoOf(ci); err == nil {
			info.Meta, info.RightToLeft = c.Meta, c.Meta.RightToLeft
		}
	}
	return info, nil
}

func comicInfoOf(f *zip.File) (metadata.ComicInfo, error) {
	rc, err := f.Open()
	if err != nil {
		return metadata.ComicInfo{}, err
	}
	defer func() { _ = rc.Close() }()
	return metadata.ParseComicInfo(io.LimitReader(rc, maxEntry))
}

func entrySize(f *zip.File) (domain.PageSize, error) {
	rc, err := f.Open()
	if err != nil {
		return domain.PageSize{}, err
	}
	defer func() { _ = rc.Close() }()
	return pageSize(rc)
}

func cbzCover(p string) ([]byte, string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = zr.Close() }()
	pages, ci := cbzPages(&zr.Reader)
	n := 0
	if ci != nil {
		if c, err := comicInfoOf(ci); err == nil && c.FrontCover >= 0 && c.FrontCover < len(pages) {
			n = c.FrontCover
		}
	}
	if len(pages) == 0 {
		return nil, "", ErrNoPage
	}
	data, err := readFile(pages[n])
	return data, imageExt(pages[n].Name), err
}

func cbzPage(p string, n int) ([]byte, string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = zr.Close() }()
	pages, _ := cbzPages(&zr.Reader)
	if n < 0 || n >= len(pages) {
		return nil, "", ErrNoPage
	}
	data, err := readFile(pages[n])
	return data, imageExt(pages[n].Name), err
}

// Archive.

func findEntry(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	// Some EPUBs get the case of their paths wrong.
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, name) {
			return f
		}
	}
	return nil
}

func readEntry(zr *zip.Reader, name string) ([]byte, error) {
	f := findEntry(zr, name)
	if f == nil {
		return nil, fmt.Errorf("%s missing from the archive", name)
	}
	return readFile(f)
}

func readFile(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxEntry {
		return nil, fmt.Errorf("%s: too large (%d bytes)", f.Name, f.UncompressedSize64)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, maxEntry))
}
