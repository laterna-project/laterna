package httpx

import (
	"net/http"

	"github.com/laterna-project/laterna/internal/i18n"
)

// Language sets the language of each request: the one its Accept-Language header asks for if the
// server has it, otherwise the server's (server is read on every request, since the setting can
// change at runtime). Handlers read it with i18n.FromContext, and authentication prefers the
// profile's when the request asked for nothing.
func Language(server func() i18n.Lang) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lang, explicit := i18n.Accept(r.Header.Get("Accept-Language"))
			if !explicit {
				lang = server()
			}
			next.ServeHTTP(w, r.WithContext(i18n.NewContext(r.Context(), lang, explicit)))
		})
	}
}
