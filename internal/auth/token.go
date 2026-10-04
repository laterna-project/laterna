package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// TokenPrefix marks a Laterna session token, which helps spot one that ended up in a log or a code
// repository by mistake.
const TokenPrefix = "lat_"

// NewToken generates a session token (256 random bits) and its hash. Only the hash is stored.
func NewToken() (token, hash string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	return token, HashToken(token), nil
}

// HashToken returns the SHA-256 of a token, in hex. A token has 256 bits of entropy, so unlike
// passwords a fast hash is enough.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// BearerToken extracts the session token from an "Authorization: Bearer <token>" header.
func BearerToken(header string) (string, bool) {
	token, ok := Bearer(header)
	return token, ok && strings.HasPrefix(token, TokenPrefix)
}

// Bearer extracts the value of an "Authorization: Bearer <token>" header, whatever the token is
// (the metrics token, for one).
func Bearer(header string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
