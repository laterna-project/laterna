package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

// ID tokens (JWT, RFC 7519) signed with RS256 or ES256, the two algorithms every OpenID Connect
// provider can produce. "none" and symmetric signatures (HS256) are rejected.

var b64 = base64.RawURLEncoding

// Claims is what Laterna keeps from an ID token.
type Claims struct {
	Issuer, Subject, Nonce string
	Audience               []string
	AuthorizedParty        string
	Expires, IssuedAt      time.Time
	// Username is "preferred_username". Email and Name are the address and display name.
	Username, Email, Name string
	EmailVerified         bool
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// splitJWT splits a compact token into header, payload and signature, and returns the signed part.
func splitJWT(token string) (jwtHeader, []byte, []byte, []byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtHeader{}, nil, nil, nil, errors.New("malformed ID token")
	}
	rawHeader, err1 := b64.DecodeString(parts[0])
	payload, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	var h jwtHeader
	if err := errors.Join(err1, err2, err3); err != nil || json.Unmarshal(rawHeader, &h) != nil {
		return jwtHeader{}, nil, nil, nil, errors.New("unreadable ID token")
	}
	return h, payload, sig, []byte(parts[0] + "." + parts[1]), nil
}

// verifySignature checks the signature of a token with the provider's key.
func verifySignature(alg string, key crypto.PublicKey, signed, sig []byte) bool {
	sum := sha256.Sum256(signed)
	switch k := key.(type) {
	case *rsa.PublicKey:
		return alg == "RS256" && rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig) == nil
	case *ecdsa.PublicKey:
		// JWS signature: r || s, 32 bytes each.
		if alg != "ES256" || len(sig) != 64 {
			return false
		}
		return ecdsa.Verify(k, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
	}
	return false
}

// parseClaims reads the payload of an ID token.
func parseClaims(payload []byte) (Claims, error) {
	var raw struct {
		Iss           string          `json:"iss"`
		Sub           string          `json:"sub"`
		Aud           json.RawMessage `json:"aud"`
		Azp           string          `json:"azp"`
		Exp           json.Number     `json:"exp"`
		Iat           json.Number     `json:"iat"`
		Nonce         string          `json:"nonce"`
		Username      string          `json:"preferred_username"`
		Email         string          `json:"email"`
		EmailVerified any             `json:"email_verified"`
		Name          string          `json:"name"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, errors.New("unreadable ID token")
	}
	c := Claims{
		Issuer: raw.Iss, Subject: raw.Sub, AuthorizedParty: raw.Azp, Nonce: raw.Nonce,
		Username: raw.Username, Email: raw.Email, Name: raw.Name,
	}
	// email_verified is a boolean, or the string "true" with some providers.
	switch v := raw.EmailVerified.(type) {
	case bool:
		c.EmailVerified = v
	case string:
		c.EmailVerified = v == "true"
	}
	var one string
	if json.Unmarshal(raw.Aud, &one) == nil {
		c.Audience = []string{one}
	} else if json.Unmarshal(raw.Aud, &c.Audience) != nil {
		return Claims{}, errors.New("unreadable token audience")
	}
	exp, err1 := raw.Exp.Float64()
	iat, err2 := raw.Iat.Float64()
	if errors.Join(err1, err2) != nil {
		return Claims{}, errors.New("unreadable token dates")
	}
	c.Expires, c.IssuedAt = time.Unix(int64(exp), 0), time.Unix(int64(iat), 0)
	return c, nil
}
