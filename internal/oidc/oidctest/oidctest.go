// Package oidctest is a fake OpenID Connect provider for tests: discovery, JWKS keys (RSA),
// authorization (the user is already signed in, so it redirects straight to the callback with a
// code) and code exchange (client secret and PKCE checked) for an RS256 ID token.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"
)

var b64 = base64.RawURLEncoding

// Provider is the fake provider.
type Provider struct {
	*httptest.Server
	ClientID, ClientSecret string
	// The user signed in at the provider.
	Subject, Username, Email, Name string
	// Refuse makes the user deny access (an error is sent to the callback).
	Refuse bool

	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]grant
}

type grant struct {
	nonce, challenge, redirect string
}

// New starts a provider for one client.
func New(clientID, clientSecret string) *Provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	p := &Provider{
		ClientID: clientID, ClientSecret: clientSecret, key: key, codes: map[string]grant{},
		Subject: "user-42", Username: "lea", Email: "lea@example.org", Name: "Léa",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	p.Server = httptest.NewServer(mux)
	return p
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token",
		"jwks_uri": p.URL + "/jwks", "response_types_supported": []string{"code"},
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "key-1", "use": "sig", "alg": "RS256",
		"n": b64.EncodeToString(p.key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(p.key.E)).Bytes()),
	}}})
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	back := url.Values{"state": {q.Get("state")}}
	if p.Refuse {
		back.Set("error", "access_denied")
	} else {
		code := rand.Text()
		p.mu.Lock()
		p.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: redirect.String()}
		p.mu.Unlock()
		back.Set("code", code)
	}
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound) //nolint:gosec // fake provider: redirects wherever the client asks
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	id, _ = url.QueryUnescape(id)
	secret, _ = url.QueryUnescape(secret)
	if !ok || id != p.ClientID || secret != p.ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "invalid_client"})
		return
	}
	code := r.PostFormValue("code")
	p.mu.Lock()
	g, ok := p.codes[code]
	delete(p.codes, code)
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if !ok || b64.EncodeToString(sum[:]) != g.challenge || r.PostFormValue("redirect_uri") != g.redirect {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	now := time.Now()
	writeJSON(w, map[string]any{"access_token": "x", "token_type": "Bearer", "id_token": p.Sign(map[string]any{
		"iss": p.URL, "sub": p.Subject, "aud": p.ClientID, "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"nonce": g.nonce, "preferred_username": p.Username, "email": p.Email, "email_verified": true, "name": p.Name,
	})})
}

// Sign signs claims as an RS256 token with the provider's key.
func (p *Provider) Sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "key-1", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signed := b64.EncodeToString(header) + "." + b64.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signed + "." + b64.EncodeToString(sig)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
