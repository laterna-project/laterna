package api

import (
	"html/template"
	"net/http"

	"github.com/laterna-project/laterna/internal/app"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/i18n"
)

// oidcPage is the page shown in the browser when the provider redirects back: the request to
// confirm the device, then the result. The login goes on in the app, which polls
// AuthService.PollOidcLogin. The form posts to the same URL (relative: the server may be served
// under a path).
var oidcPage = template.Must(template.New("oidc").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root { color-scheme: dark; }
  body { font-family: system-ui, sans-serif; display: grid; place-items: center; min-height: 100vh; margin: 0;
         background: #111; color: #eee; padding: 16px; box-sizing: border-box; }
  main { max-width: 32rem; text-align: center; }
  h1 { font-size: 1.4rem; }
  .hint { color: #aaa; font-size: .9rem; }
  form { display: flex; gap: 12px; justify-content: center; margin-top: 24px; flex-wrap: wrap; }
  button { font: inherit; padding: 10px 20px; border-radius: 8px; border: 1px solid #555; background: #222; color: #eee; cursor: pointer; }
  button[value=approve] { background: #e8b04a; border-color: #e8b04a; color: #111; font-weight: 600; }
</style></head>
<body><main><h1>{{.Title}}</h1>
{{- if .Confirm}}
<p>{{.Question}}</p>
<p class="hint">{{.Hint}}</p>
<form method="post" action="callback">
  <input type="hidden" name="confirm" value="{{.Confirm}}">
  <button type="submit" name="action" value="deny">{{.Deny}}</button>
  <button type="submit" name="action" value="approve">{{.Approve}}</button>
</form>
{{- else}}
<p>{{.Message}}</p>
{{- end}}
</main></body>
</html>`))

// oidcView holds the texts of the page, already written in the browser's language.
type oidcView struct {
	Lang, Title, Message          string
	Confirm                       string
	Question, Hint, Deny, Approve string
}

// oidcFailed is the page for a refusal, oidcDone the one for a successful login.
func oidcFailed(r *http.Request, msg domain.Text) oidcView {
	lang := i18n.FromContext(r.Context())
	return oidcView{Lang: string(lang), Title: i18n.Text(lang, "oidc.page.title_failed"), Message: i18n.Render(lang, msg)}
}

func oidcDone(r *http.Request, msg domain.Text) oidcView {
	lang := i18n.FromContext(r.Context())
	return oidcView{Lang: string(lang), Title: i18n.Text(lang, "oidc.page.title"), Message: i18n.Render(lang, msg)}
}

// oidcCallback receives the provider's redirect: GET /auth/oidc/callback?code=...&state=... (or
// ?error=...), protected by the random state as the code flow requires. It asks to confirm the
// device. The answer comes back as a POST, protected by the confirmation token only this page
// knows.
func oidcCallback(a *app.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cb := a.CompleteOIDCLogin(r.Context(), q.Get("state"), q.Get("code"), q.Get("error"))
		if cb.Confirm == "" {
			writeOIDCPage(w, http.StatusBadRequest, oidcFailed(r, cb.Message))
			return
		}
		lang := i18n.FromContext(r.Context())
		writeOIDCPage(w, http.StatusOK, oidcView{
			Lang: string(lang), Title: i18n.Text(lang, "oidc.page.title"), Confirm: cb.Confirm,
			Question: i18n.Text(lang, "oidc.page.confirm", "device", cb.Device.Name, "client", cb.Device.Client, "ip", cb.IP, "account", cb.Account),
			Hint:     i18n.Text(lang, "oidc.page.hint"), Deny: i18n.Text(lang, "oidc.page.deny"), Approve: i18n.Text(lang, "oidc.page.approve"),
		})
	})
}

// oidcConfirm receives the answer from the confirmation page: POST /auth/oidc/callback.
func oidcConfirm(a *app.App) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := r.ParseForm(); err != nil {
			writeOIDCPage(w, http.StatusBadRequest, oidcFailed(r, domain.T("oidc.page.invalid_request")))
			return
		}
		msg, ok := a.ConfirmOIDCLogin(r.Context(), r.PostForm.Get("confirm"), r.PostForm.Get("action") == "approve")
		if !ok {
			writeOIDCPage(w, http.StatusBadRequest, oidcFailed(r, msg))
			return
		}
		writeOIDCPage(w, http.StatusOK, oidcDone(r, msg))
	})
}

// writeOIDCPage writes the page: no caching, no referrer (the URL carries the code), no framing
// (the confirmation must not be clickable behind the user's back), no script.
func writeOIDCPage(w http.ResponseWriter, status int, v oidcView) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = oidcPage.Execute(w, v)
}
