package domain

import "time"

// BookFormat is the format of a book file.
type BookFormat string

// Book formats.
const (
	BookEPUB BookFormat = "epub"
	BookPDF  BookFormat = "pdf"
	BookCBZ  BookFormat = "cbz"
)

// BookLayout is how a book file is read. It is decided when the file is analyzed.
type BookLayout string

// Book layouts.
const (
	// LayoutImages is one image per page, served at the requested width (CBZ, scanned PDF).
	LayoutImages BookLayout = "images"
	// LayoutReflowable is text laid out by the reader from the file itself (EPUB).
	LayoutReflowable BookLayout = "reflowable"
	// LayoutDocument is fixed pages rendered by the reader from the file itself (PDF).
	LayoutDocument BookLayout = "document"
)

// Book is the book-specific part of an item. Its parent is its series.
type Book struct {
	ItemID ID
	// Number is the volume in the series (0.5 or 12.5 happen); 0 if standalone or unknown.
	Number    float64
	Publisher string
	// Language of the text (BCP 47), empty if unknown.
	Language string
	ISBN     string
}

// PageSize is the size of a page in pixels.
type PageSize struct {
	Width, Height int
}

// BookFile says how to read a book file.
type BookFile struct {
	FileID      ID
	Format      BookFormat
	Layout      BookLayout
	RightToLeft bool
	PageCount   int
	// Pages holds the size of each page (LayoutImages only).
	Pages []PageSize
	// Key is part of the URLs of the file and its pages and stands in for authentication.
	Key string
}

// ReadingProgress is where a profile is in a book.
type ReadingProgress struct {
	// Page is zero-based, for image and document layouts.
	Page int
	// Locator is whatever the reader gave us (an EPUB CFI). The server does not look inside.
	Locator string
	// Progression goes from 0 to 1.
	Progression float64
	UpdatedAt   time.Time
}

// ReadingFinished is the progression past which a book counts as read.
const ReadingFinished = 0.98
