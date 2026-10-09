package api

import (
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/laterna-project/laterna/internal/api/httpx"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// requestPosters serves the posters of request search results and of requests: GET
// /requests/posters/{key}. No authentication, like an <img> element: the key is the hash of an
// address a search returned or a request keeps, and the server only fetches those. A key always
// names the same image, hence long caching.
func requestPosters(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, err := a.RequestPoster(r.Context(), r.PathValue("key"))
		var de *domain.Error
		switch {
		case errors.As(err, &de) && errors.Is(de.Kind, domain.ErrNotFound):
			http.NotFound(w, r)
			return
		case err != nil:
			log.ErrorContext(r.Context(), "cannot serve request poster", "err", err, "request_id", httpx.RequestIDFrom(r.Context()))
			internalError(w, r)
			return
		}
		f, err := os.Open(path) //nolint:gosec // G703: a cache path, from a key of 32 hex digits checked by RequestPoster
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = f.Close() }()
		st, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=604800")
		http.ServeContent(w, r, "", st.ModTime(), f)
	})
}
