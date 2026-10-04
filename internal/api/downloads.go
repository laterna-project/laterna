package api

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/laterna-project/laterna/internal/api/httpx"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/auth"
	"github.com/laterna-project/laterna/internal/domain"
)

// bearer authenticates a request with the "Authorization: Bearer <token>" header (routes outside
// Connect that serve private files). On failure the response is written and ok is false.
func bearer(a *app.App, log *slog.Logger, w http.ResponseWriter, r *http.Request) (domain.Principal, bool) {
	token, ok := auth.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, r, http.StatusUnauthorized, domain.T("error.auth.required"))
		return domain.Principal{}, false
	}
	ip := httpx.ClientIPFrom(r.Context())
	p, err := a.Authenticate(r.Context(), token, ip)
	var de *domain.Error
	switch {
	case errors.As(err, &de):
		writeError(w, r, http.StatusUnauthorized, de.Text())
		return domain.Principal{}, false
	case err != nil:
		log.ErrorContext(r.Context(), "authentication failed", "path", r.URL.Path, "err", err, "request_id", httpx.RequestIDFrom(r.Context()))
		internalError(w, r)
		return domain.Principal{}, false
	}
	return p, true
}

// downloadFile serves the file of a ready offline download to the device that asked for it, and
// accepts Range requests (an interrupted download resumes):
//
//	GET /downloads/{id}/file
func downloadFile(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := bearer(a, log, w, r)
		if !ok {
			return
		}
		id, err := domain.ParseID(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path, name, err := a.DownloadFile(r.Context(), p, id)
		if err == nil {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		}
		serveFile(w, r, log, path, mime.TypeByExtension(strings.ToLower(filepath.Ext(path))), "private, no-cache", err)
	})
}

// downloadSubtitle serves a subtitle of a download ("3.vtt"):
//
//	GET /downloads/{id}/subtitles/{position}.{format}
func downloadSubtitle(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := bearer(a, log, w, r)
		if !ok {
			return
		}
		id, err := domain.ParseID(r.PathValue("id"))
		pos, format, found := strings.Cut(r.PathValue("file"), ".")
		n, convErr := strconv.Atoi(pos)
		contentType, known := subtitleTypes[format]
		if err != nil || !found || convErr != nil || !known {
			http.NotFound(w, r)
			return
		}
		path, err := a.DownloadSubtitle(r.Context(), p, id, n, format)
		serveFile(w, r, log, path, contentType, "private, no-cache", err)
	})
}
