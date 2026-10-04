package api

import (
	"log/slog"
	"net/http"

	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// backupFiles serves a database backup: GET /backups/{name}, so that it can be stored somewhere
// safe. Only for administrators on an unrestricted profile, with the "Authorization: Bearer"
// header: a backup holds everything, password hashes included.
func backupFiles(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := bearer(a, log, w, r)
		if !ok {
			return
		}
		if !p.CanAdminister() {
			writeError(w, r, http.StatusForbidden, domain.T("error.auth.admin_only"))
			return
		}
		f, err := a.OpenBackup(r.PathValue("file"))
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
		h.Set("Content-Type", "application/vnd.sqlite3")
		h.Set("Content-Disposition", `attachment; filename="`+st.Name()+`"`)
		h.Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "", st.ModTime(), f)
	})
}
