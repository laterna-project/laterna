package rpc

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// BookService implements laterna.v1.BookService.
type BookService struct {
	app *app.App
}

var bookFormats = map[domain.BookFormat]laternav1.BookFormat{
	domain.BookEPUB: laternav1.BookFormat_BOOK_FORMAT_EPUB,
	domain.BookPDF:  laternav1.BookFormat_BOOK_FORMAT_PDF,
	domain.BookCBZ:  laternav1.BookFormat_BOOK_FORMAT_CBZ,
}

var bookLayouts = map[domain.BookLayout]laternav1.BookLayout{
	domain.LayoutImages:     laternav1.BookLayout_BOOK_LAYOUT_IMAGES,
	domain.LayoutReflowable: laternav1.BookLayout_BOOK_LAYOUT_REFLOWABLE,
	domain.LayoutDocument:   laternav1.BookLayout_BOOK_LAYOUT_DOCUMENT,
}

func bookFileMsg(f domain.BookFile) *laternav1.BookFile {
	return &laternav1.BookFile{
		Format: bookFormats[f.Format], Layout: bookLayouts[f.Layout], RightToLeft: f.RightToLeft, PageCount: clampInt32(f.PageCount),
	}
}

func readingMsg(p *domain.ReadingProgress) *laternav1.ReadingProgress {
	if p == nil {
		return nil
	}
	return &laternav1.ReadingProgress{
		Page: clampInt32(p.Page), Locator: p.Locator, Progression: p.Progression, UpdatedAt: timestamppb.New(p.UpdatedAt),
	}
}

func bookSeriesMsg(v domain.ItemView) *laternav1.BookSeries {
	it := v.Item
	return &laternav1.BookSeries{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: it.Title, Images: imagesMsg(v.Images),
		BookCount: clampInt32(v.BookCount), UserData: userDataMsg(v.UserData), AddedAt: timestamppb.New(it.AddedAt),
	}
}

func bookSummaryMsg(ctx context.Context, v domain.ItemView) *laternav1.BookSummary {
	it := v.Item
	msg := &laternav1.BookSummary{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: it.Title, SeriesTitle: v.SeriesTitle, Year: clampInt32(it.Year),
		Images: imagesMsg(v.Images), UserData: userDataMsg(v.UserData), Progress: readingMsg(v.Reading), AddedAt: timestamppb.New(it.AddedAt),
	}
	if it.ParentID != nil {
		msg.SeriesId = it.ParentID.String()
	}
	if v.Book != nil {
		msg.Number = v.Book.Number
		name, ok := domain.BookName(it.Title, v.SeriesTitle, v.Book.Number)
		msg.Title, msg.TitleText = given(ctx, it.Title, name, ok)
	}
	return msg
}

// ListBookSeries returns a page of book series.
func (s *BookService) ListBookSeries(ctx context.Context, req *connect.Request[laternav1.ListBookSeriesRequest]) (*connect.Response[laternav1.ListBookSeriesResponse], error) {
	m := req.Msg
	lib, err := parseOptionalID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	page, err := s.app.ListBookSeries(ctx, principal(ctx), app.ListQuery{
		LibraryID: lib, Sort: itemSortFromMsg(m.GetSort()), Reverse: m.GetReverse(),
		FavoritesOnly: m.GetFavoritesOnly(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.BookSeries, len(page.Items))
	for i, v := range page.Items {
		out[i] = bookSeriesMsg(v)
	}
	return connect.NewResponse(&laternav1.ListBookSeriesResponse{
		Series: out, NextPageToken: page.NextPageToken, TotalSize: clampInt32(page.Total),
	}), nil
}

// GetBookSeries returns a series and its books.
func (s *BookService) GetBookSeries(ctx context.Context, req *connect.Request[laternav1.GetBookSeriesRequest]) (*connect.Response[laternav1.GetBookSeriesResponse], error) {
	id, err := parseID(req.Msg.GetSeriesId(), "series_id")
	if err != nil {
		return nil, err
	}
	v, list, err := s.app.BookSeries(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.GetBookSeriesResponse{Series: bookSeriesMsg(v)}
	for _, b := range list {
		resp.Books = append(resp.Books, bookSummaryMsg(ctx, b))
	}
	return connect.NewResponse(resp), nil
}

// ListBooks returns a page of books.
func (s *BookService) ListBooks(ctx context.Context, req *connect.Request[laternav1.ListBooksRequest]) (*connect.Response[laternav1.ListBooksResponse], error) {
	m := req.Msg
	lib, err := parseOptionalID(m.GetLibraryId(), "library_id")
	if err != nil {
		return nil, err
	}
	series, err := parseOptionalID(m.GetSeriesId(), "series_id")
	if err != nil {
		return nil, err
	}
	page, err := s.app.ListBooks(ctx, principal(ctx), app.ListQuery{
		LibraryID: lib, SeriesID: series, Sort: itemSortFromMsg(m.GetSort()), Reverse: m.GetReverse(), Genre: m.GetGenre(),
		Played: m.Read, FavoritesOnly: m.GetFavoritesOnly(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*laternav1.BookSummary, len(page.Items))
	for i, v := range page.Items {
		out[i] = bookSummaryMsg(ctx, v)
	}
	return connect.NewResponse(&laternav1.ListBooksResponse{
		Books: out, NextPageToken: page.NextPageToken, TotalSize: clampInt32(page.Total),
	}), nil
}

// GetBook returns the details of a book and its files.
func (s *BookService) GetBook(ctx context.Context, req *connect.Request[laternav1.GetBookRequest]) (*connect.Response[laternav1.GetBookResponse], error) {
	id, err := parseID(req.Msg.GetBookId(), "book_id")
	if err != nil {
		return nil, err
	}
	v, d, err := s.app.Book(ctx, principal(ctx), id)
	if err != nil {
		return nil, err
	}
	it := v.Item
	summary := bookSummaryMsg(ctx, v)
	book := &laternav1.Book{
		Id: it.ID.String(), LibraryId: it.LibraryID.String(), Title: summary.GetTitle(), TitleText: summary.GetTitleText(),
		SeriesTitle: v.SeriesTitle, Year: clampInt32(it.Year),
		PremiereDate: it.PremiereDate, Overview: it.Overview, Genres: d.Genres, Credits: creditsMsg(d.Credits),
		Images: imagesMsg(v.Images), UserData: userDataMsg(v.UserData), Progress: readingMsg(v.Reading), AddedAt: timestamppb.New(it.AddedAt),
	}
	if it.ParentID != nil {
		book.SeriesId = it.ParentID.String()
	}
	if b := v.Book; b != nil {
		book.Number, book.Publisher, book.Language, book.Isbn = b.Number, b.Publisher, b.Language, b.ISBN
	}
	return connect.NewResponse(&laternav1.GetBookResponse{Book: book, Files: filesMsg(d.Files)}), nil
}

// bookURL is the URL of a book file. The key stands in for authentication.
func bookURL(f domain.BookFile) string { return fmt.Sprintf("/books/%s/%s", f.FileID, f.Key) }

// OpenBook prepares reading a book.
func (s *BookService) OpenBook(ctx context.Context, req *connect.Request[laternav1.OpenBookRequest]) (*connect.Response[laternav1.OpenBookResponse], error) {
	id, err := parseID(req.Msg.GetBookId(), "book_id")
	if err != nil {
		return nil, err
	}
	fileID, err := parseOptionalID(req.Msg.GetFileId(), "file_id")
	if err != nil {
		return nil, err
	}
	r, err := s.app.OpenBook(ctx, principal(ctx), id, fileID)
	if err != nil {
		return nil, err
	}
	resp := &laternav1.OpenBookResponse{
		Book: bookSummaryMsg(ctx, r.View), FileId: r.File.FileID.String(), File: bookFileMsg(r.File),
		FileUrl: bookURL(r.File) + "/file", Progress: readingMsg(r.Progress),
	}
	if r.File.Layout == domain.LayoutImages {
		resp.PageUrlPrefix = bookURL(r.File) + "/pages/"
		for _, p := range r.File.Pages {
			resp.Pages = append(resp.Pages, &laternav1.PageSize{Width: clampInt32(p.Width), Height: clampInt32(p.Height)})
		}
	}
	return connect.NewResponse(resp), nil
}

// SaveReadingProgress stores where the profile is in a book.
func (s *BookService) SaveReadingProgress(ctx context.Context, req *connect.Request[laternav1.SaveReadingProgressRequest]) (*connect.Response[laternav1.SaveReadingProgressResponse], error) {
	m := req.Msg
	id, err := parseID(m.GetBookId(), "book_id")
	if err != nil {
		return nil, err
	}
	if err := s.app.SaveReadingProgress(ctx, principal(ctx), id, domain.ReadingProgress{
		Page: int(m.GetPage()), Locator: m.GetLocator(), Progression: m.GetProgression(),
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.SaveReadingProgressResponse{}), nil
}
