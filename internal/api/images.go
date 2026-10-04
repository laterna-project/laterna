package api

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/laterna-project/laterna/internal/api/httpx"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// images serves the catalog images: GET /images/{id}/{hash}[?w=width]. No authentication, like an
// <img> element: the URL, which holds the ID and the content hash, cannot be guessed. It changes
// when the image changes, hence unlimited caching by clients and proxies.
func images(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := domain.ParseID(r.PathValue("id"))
		if err != nil {
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
		img, err := a.Image(r.Context(), id, r.PathValue("hash"), width)
		var de *domain.Error
		switch {
		case errors.As(err, &de) && errors.Is(de.Kind, domain.ErrNotFound):
			http.NotFound(w, r)
			return
		case errors.As(err, &de):
			writeError(w, r, http.StatusBadRequest, de.Text())
			return
		case err != nil:
			log.ErrorContext(r.Context(), "cannot serve image", "image", id, "err", err, "request_id", httpx.RequestIDFrom(r.Context()))
			internalError(w, r)
			return
		}
		f, err := os.Open(img.Path)
		if err != nil {
			// The file is gone since the analysis (share down, image deleted): the next scan will
			// update the item.
			log.WarnContext(r.Context(), "unreadable image", "image", id, "err", err)
			http.NotFound(w, r)
			return
		}
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("ETag", img.ETag)
		if img.ContentType != "" {
			h.Set("Content-Type", img.ContentType)
		}
		http.ServeContent(w, r, "", st.ModTime(), f)
	})
}
