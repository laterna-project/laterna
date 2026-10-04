package api

import (
	"log/slog"
	"net/http"

	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// logFiles serves a daily log file: GET /logs/{name}. Only for administrators on an unrestricted
// profile, with the "Authorization: Bearer" header.
func logFiles(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := bearer(a, log, w, r)
		if !ok {
			return
		}
		if !p.CanAdminister() {
			writeError(w, r, http.StatusForbidden, domain.T("error.auth.admin_only"))
			return
		}
		f, err := a.OpenLogFile(r.PathValue("file"))
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
		h := w.Header()
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="`+st.Name()+`"`)
		h.Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "", st.ModTime(), f)
	})
}
