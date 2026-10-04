package api

import (
	"net/http"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
)

// ErrorHeader carries the code of an expected error on routes that do not go through Connect. The
// body is its text, written in the language of the request.
const ErrorHeader = "Laterna-Error"

// writeError answers with an expected error: t is its text (key "error.<code>").
func writeError(w http.ResponseWriter, r *http.Request, status int, t domain.Text) {
	w.Header().Set(ErrorHeader, strings.TrimPrefix(t.Key, "error."))
	http.Error(w, i18n.Render(i18n.FromContext(r.Context()), t), status)
}

// internalError answers with the unexpected error, saying nothing about it (it is logged).
func internalError(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusInternalServerError, domain.T("error.server.internal"))
}
