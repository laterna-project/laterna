package api

import (
	"bytes"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// Books: like images, no authentication. The key in the URL, drawn at random when the file is
// analyzed, cannot be guessed, and the URL never serves other content.

const immutable = "public, max-age=31536000, immutable"

// bookFile serves the whole file of a book (EPUB, PDF, CBZ) and accepts Range requests.
//
//	GET /books/{file}/{key}/file
func bookFile(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := domain.ParseID(r.PathValue("file"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path, name, contentType, err := a.BookContent(r.Context(), id, r.PathValue("key"))
		if err == nil {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
		}
		serveFile(w, r, log, path, contentType, immutable, err)
	})
}

// bookPage serves a page of a book read page by page, resized if "?w=" asks for it.
//
//	GET /books/{file}/{key}/pages/{n}[?w=width]
func bookPage(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := domain.ParseID(r.PathValue("file"))
		n, convErr := strconv.Atoi(r.PathValue("page"))
		if err != nil || convErr != nil {
			http.NotFound(w, r)
			return
		}
		width := 0
		if v := r.URL.Query().Get("w"); v != "" {
			if width, err = strconv.Atoi(v); err != nil {
				writeError(w, r, http.StatusBadRequest, domain.T("error.request.invalid_width"))
				return
			}
		}
		page, err := a.BookPage(r.Context(), id, r.PathValue("key"), n, width)
		var de *domain.Error
		if errors.As(err, &de) && errors.Is(de.Kind, domain.ErrInvalid) {
			writeError(w, r, http.StatusBadRequest, de.Text())
			return
		}
		if err != nil || page.Path != "" {
			if err == nil {
				w.Header().Set("ETag", page.ETag)
			}
			serveFile(w, r, log, page.Path, page.ContentType, immutable, err)
			return
		}
		// Original page, read from the archive.
		h := w.Header()
		h.Set("Cache-Control", immutable)
		h.Set("ETag", page.ETag)
		if page.ContentType != "" {
			h.Set("Content-Type", page.ContentType)
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(page.Data))
	})
}
