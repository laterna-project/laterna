package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// trickplaySheets serves a sheet of scrubbing thumbnails. Like images: no authentication, the URL
// (with a key drawn at random for each generation) cannot be guessed and never changes for the same
// content.
//
//	GET /trickplay/{file}/{key}/{n}.jpg
func trickplaySheets(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := domain.ParseID(r.PathValue("file"))
		name, ok := strings.CutSuffix(r.PathValue("sheet"), ".jpg")
		n, convErr := strconv.Atoi(name)
		if err != nil || !ok || convErr != nil {
			http.NotFound(w, r)
			return
		}
		path, err := a.TrickplaySheet(r.Context(), id, r.PathValue("key"), n)
		serveFile(w, r, log, path, "image/jpeg", "public, max-age=31536000, immutable", err)
	})
}
