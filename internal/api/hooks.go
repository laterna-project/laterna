package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/laterna-project/laterna/internal/api/httpx"
	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
)

// maxHookBody caps the body of a webhook (a few KB in practice).
const maxHookBody = 1 << 20

// hooks receives the Sonarr and Radarr webhooks: POST /hooks/{sonarr|radarr}. Basic auth carries
// the secret handed to the instance when the webhook was installed.
func hooks(a *app.App, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, secret, _ := r.BasicAuth()
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
		if err != nil {
			writeError(w, r, http.StatusRequestEntityTooLarge, domain.T("error.request.body_too_large"))
			return
		}
		err = a.ArrWebhook(r.Context(), r.PathValue("kind"), secret, body)
		var de *domain.Error
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.As(err, &de) && errors.Is(de.Kind, domain.ErrNotFound):
			http.NotFound(w, r)
		case errors.As(err, &de) && errors.Is(de.Kind, domain.ErrUnauthenticated):
			w.Header().Set("WWW-Authenticate", `Basic realm="Laterna"`)
			writeError(w, r, http.StatusUnauthorized, de.Text())
		case errors.As(err, &de):
			writeError(w, r, http.StatusBadRequest, de.Text())
		default:
			log.ErrorContext(r.Context(), "cannot handle webhook", "kind", r.PathValue("kind"), "err", err, "request_id", httpx.RequestIDFrom(r.Context()))
			internalError(w, r)
		}
	})
}
