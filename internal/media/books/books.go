// Package books reads book files: EPUB and CBZ (ZIP archives) and PDF. It decides how they are read
// and gives their pages and cover. The metadata they hold is interpreted by package metadata.
package books

import (
	"archive/zip"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders registered with image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/metadata"
)

// Info is what the analysis keeps from a book file.
type Info struct {
	Format      domain.BookFormat
	Layout      domain.BookLayout
	RightToLeft bool
	PageCount   int
	// Pages holds the size of each page (LayoutImages).
	Pages []domain.PageSize
	// Meta is what the file says about itself.
	Meta metadata.BookMeta
	// HasCover means Cover will find a cover.
	HasCover bool
}

// Limits against a malicious file.
const (
	// maxEntry is the uncompressed size of an entry read in full (page, cover, OPF).
	maxEntry = 64 << 20
	// maxPages is the number of pages of a book.
	maxPages = 10_000
	// maxPagePixels caps the dimensions of a page (same as catalog images).
	maxPagePixels = 80_000_000
)

// ErrNoPage means the requested page does not exist, or the book is not read page by page.
var ErrNoPage = errors.New("page not found")

// FormatOf returns the format of a file from its extension.
func FormatOf(path string) (domain.BookFormat, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".epub":
		return domain.BookEPUB, true
	case ".pdf":
		return domain.BookPDF, true
	case ".cbz":
		return domain.BookCBZ, true
	}
	return "", false
}

// Read analyzes a book file.
func Read(path string) (Info, error) {
	format, ok := FormatOf(path)
	if !ok {
		return Info{}, fmt.Errorf("%s: unknown book format", path)
	}
	var (
		info Info
		err  error
	)
	switch format {
	case domain.BookEPUB:
		info, err = readEPUB(path)
	case domain.BookCBZ:
		info, err = readCBZ(path)
	case domain.BookPDF:
		info, err = readPDF(path)
	}
	if err != nil {
		return Info{}, fmt.Errorf("unreadable book %s: %w", path, err)
	}
	info.Format = format
	return info, nil
}

// ReadMeta only reads what a file says about itself (OPF, ComicInfo.xml, Info) without going
// through its pages. It is used to refresh the metadata of a book that was already analyzed.
func ReadMeta(path string) (metadata.BookMeta, error) {
	format, _ := FormatOf(path)
	switch format {
	case domain.BookEPUB:
		zr, err := zip.OpenReader(path)
		if err != nil {
			return metadata.BookMeta{}, err
		}
		defer func() { _ = zr.Close() }()
		opf, _, err := epubOPF(&zr.Reader)
		return opf.Meta, err
	case domain.BookCBZ:
		zr, err := zip.OpenReader(path)
		if err != nil {
			return metadata.BookMeta{}, err
		}
		defer func() { _ = zr.Close() }()
		if _, ci := cbzPages(&zr.Reader); ci != nil {
			c, err := comicInfoOf(ci)
			return c.Meta, err
		}
		return metadata.BookMeta{}, nil
	case domain.BookPDF:
		f, err := os.Open(path)
		if err != nil {
			return metadata.BookMeta{}, err
		}
		defer func() { _ = f.Close() }()
		d, err := openPDF(f)
		if err != nil || d.trailer["Encrypt"] != nil {
			return metadata.BookMeta{}, nil // an unreadable PDF says nothing about itself
		}
		return metadata.PDFMeta(d.infoStrings()), nil
	}
	return metadata.BookMeta{}, fmt.Errorf("%s: unknown book format", path)
}

// Cover returns the cover image of a file (bytes and extension, ".jpg").
func Cover(path string) ([]byte, string, error) {
	format, _ := FormatOf(path)
	switch format {
	case domain.BookEPUB:
		return epubCover(path)
	case domain.BookCBZ:
		return cbzCover(path)
	case domain.BookPDF:
		return Page(path, 0)
	}
	return nil, "", fmt.Errorf("%s: unknown book format", path)
}

// Page returns the image of page n (zero-based) of a book read page by page: bytes and extension.
// ErrNoPage if there is no such page.
func Page(path string, n int) ([]byte, string, error) {
	format, _ := FormatOf(path)
	switch format {
	case domain.BookCBZ:
		return cbzPage(path, n)
	case domain.BookPDF:
		return pdfPage(path, n)
	case domain.BookEPUB:
	}
	return nil, "", ErrNoPage
}

// pageSize reads the dimensions of an image without decoding it.
func pageSize(r interface{ Read([]byte) (int, error) }) (domain.PageSize, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return domain.PageSize{}, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPagePixels {
		return domain.PageSize{}, fmt.Errorf("dimensions %d×%d not accepted", cfg.Width, cfg.Height)
	}
	return domain.PageSize{Width: cfg.Width, Height: cfg.Height}, nil
}

// imageExt returns the extension of a known image name, "" otherwise.
func imageExt(name string) string {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".jpg", ".jpeg":
		return ".jpg"
	case ".png", ".webp", ".gif":
		return ext
	}
	return ""
}
