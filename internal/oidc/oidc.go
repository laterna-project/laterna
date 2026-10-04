// Package oidc connects Laterna to an OpenID Connect provider: Authelia, Authentik, Keycloak,
// Google... It discovers the provider, builds the authorization URL (code flow with PKCE and a
// nonce), exchanges the code for an ID token and verifies it: signature (JWKS keys, RS256 or
// ES256), issuer, audience, expiry, nonce. It knows nothing about the database or accounts.
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// skew is the clock difference we tolerate with the provider.
	skew = time.Minute
	// refetchKeys is the minimum time between two key fetches (an unknown key means a rotation).
	refetchKeys = time.Minute
	maxBody     = 1 << 20
)

// Provider is a discovered provider.
type Provider struct {
	Issuer   string
	AuthURL  string
	TokenURL string
	JWKSURL  string

	client *http.Client
	mu     sync.Mutex
	keys   map[string]crypto.PublicKey
	keysAt time.Time
}

// Discover reads the provider's configuration (/.well-known/openid-configuration) and checks that
// it announces this very issuer.
func Discover(ctx context.Context, client *http.Client, issuer string) (*Provider, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("invalid provider address: \"https://auth.example.com\" expected")
	}
	var conf struct {
		Issuer   string   `json:"issuer"`
		Auth     string   `json:"authorization_endpoint"`
		Token    string   `json:"token_endpoint"`
		JWKS     string   `json:"jwks_uri"`
		Response []string `json:"response_types_supported"`
	}
	if err := getJSON(ctx, client, issuer+"/.well-known/openid-configuration", &conf); err != nil {
		return nil, fmt.Errorf("provider configuration: %w", err)
	}
	if strings.TrimRight(conf.Issuer, "/") != issuer {
		return nil, fmt.Errorf("the provider announces itself as \"%s\", not \"%s\"", conf.Issuer, issuer)
	}
	if conf.Auth == "" || conf.Token == "" || conf.JWKS == "" {
		return nil, errors.New("incomplete provider configuration")
	}
	if len(conf.Response) > 0 && !slices.Contains(conf.Response, "code") {
		return nil, errors.New("the provider does not offer the authorization code flow")
	}
	return &Provider{Issuer: conf.Issuer, AuthURL: conf.Auth, TokenURL: conf.Token, JWKSURL: conf.JWKS, client: client}, nil
}

// Secret returns a random opaque value (state, nonce, PKCE verifier): 32 bytes as base64url.
func Secret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b64.EncodeToString(b)
}

// AuthCodeURL returns the authorization URL: code flow, PKCE (S256), nonce.
func (p *Provider) AuthCodeURL(clientID, redirectURI, state, nonce, verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI},
		"scope": {"openid profile email"}, "state": {state}, "nonce": {nonce},
		"code_challenge": {b64.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(p.AuthURL, "?") {
		sep = "&"
	}
	return p.AuthURL + sep + q.Encode()
}

// Exchange trades the authorization code for the ID token (client secret sent as Basic auth).
func (p *Provider) Exchange(ctx context.Context, clientID, clientSecret, code, verifier, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("code exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", err
	}
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK || out.IDToken == "" {
		return "", fmt.Errorf("code exchange refused (%d): %s %s", resp.StatusCode, out.Error, out.Desc)
	}
	return out.IDToken, nil
}

// Verify checks an ID token: signature, issuer, audience (and authorized party if there are
// several), expiry, nonce.
func (p *Provider) Verify(ctx context.Context, token, clientID, nonce string, now time.Time) (Claims, error) {
	h, payload, sig, signed, err := splitJWT(token)
	if err != nil {
		return Claims{}, err
	}
	if h.Alg != "RS256" && h.Alg != "ES256" {
		return Claims{}, fmt.Errorf("signature %q not accepted (RS256 or ES256 expected)", h.Alg)
	}
	key, err := p.key(ctx, h.Kid, now)
	if err != nil {
		return Claims{}, err
	}
	if !verifySignature(h.Alg, key, signed, sig) {
		return Claims{}, errors.New("wrong ID token signature")
	}
	c, err := parseClaims(payload)
	switch {
	case err != nil:
		return Claims{}, err
	case c.Issuer != p.Issuer:
		return Claims{}, errors.New("ID token from another issuer")
	case !slices.Contains(c.Audience, clientID):
		return Claims{}, errors.New("ID token meant for another client")
	case len(c.Audience) > 1 && c.AuthorizedParty != clientID:
		return Claims{}, errors.New("ID token issued to another client")
	case now.After(c.Expires.Add(skew)):
		return Claims{}, errors.New("expired ID token")
	case c.IssuedAt.After(now.Add(skew)):
		return Claims{}, errors.New("ID token dated in the future")
	case c.Nonce != nonce:
		return Claims{}, errors.New("ID token of another request (nonce)")
	case c.Subject == "":
		return Claims{}, errors.New("ID token without a subject")
	}
	return c, nil
}

// key returns the provider's key kid. Keys are fetched again if it is unknown (rotation), at most
// once a minute.
func (p *Provider) key(ctx context.Context, kid string, now time.Time) (crypto.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k, ok := p.pick(kid); ok {
		return k, nil
	}
	if p.keys != nil && now.Sub(p.keysAt) < refetchKeys {
		return nil, errors.New("signing key unknown to the provider")
	}
	keys, err := fetchKeys(ctx, p.client, p.JWKSURL)
	if err != nil {
		return nil, err
	}
	p.keys, p.keysAt = keys, now
	if k, ok := p.pick(kid); ok {
		return k, nil
	}
	return nil, errors.New("signing key unknown to the provider")
}

// pick returns the key kid, or the only key if the token names none.
func (p *Provider) pick(kid string) (crypto.PublicKey, bool) {
	if k, ok := p.keys[kid]; ok {
		return k, true
	}
	if kid == "" && len(p.keys) == 1 {
		for _, k := range p.keys {
			return k, true
		}
	}
	return nil, false
}

// fetchKeys reads the provider's public keys (JWKS): RSA and EC P-256 signing keys.
func fetchKeys(ctx context.Context, client *http.Client, jwksURL string) (map[string]crypto.PublicKey, error) {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := getJSON(ctx, client, jwksURL, &set); err != nil {
		return nil, fmt.Errorf("provider keys: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := b64.DecodeString(k.N)
			e, err2 := b64.DecodeString(k.E)
			if errors.Join(err1, err2) != nil || len(n) < 256 || len(e) == 0 || len(e) > 4 {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case "EC":
			x, err1 := b64.DecodeString(k.X)
			y, err2 := b64.DecodeString(k.Y)
			if k.Crv != "P-256" || errors.Join(err1, err2) != nil || len(x) != 32 || len(y) != 32 {
				continue
			}
			key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
			if err == nil {
				keys[k.Kid] = key
			}
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("the provider has no usable signing key")
	}
	return keys, nil
}

func getJSON(ctx context.Context, client *http.Client, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(dst)
}
