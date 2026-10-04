package auth

import (
	"crypto/pbkdf2"
	"crypto/sha1" //nolint:gosec // PBKDF2-SHA1 from old Jellyfin versions, only ever verified
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"hash"
	"strconv"
	"strings"
)

// Hashes imported from Jellyfin: $PBKDF2-SHA512$iterations=210000$<salt>$<hash> (salt and hash in
// hex), or $PBKDF2$... with SHA-1 for its older versions. They are verified as they are and
// replaced by an argon2id hash on the first successful login (needsRehash).

// Bounds on the iteration count we accept: an imported hash must not take a minute to check.
const (
	minIterations = 1000
	maxIterations = 1_000_000
)

// IsLegacyHash reports an imported hash that VerifyPassword can check.
func IsLegacyHash(encoded string) bool {
	_, _, _, _, err := decodePBKDF2(encoded)
	return err == nil
}

func verifyPBKDF2(encoded, secret string) (ok bool, err error) {
	h, iter, salt, key, err := decodePBKDF2(encoded)
	if err != nil {
		return false, err
	}
	got, err := pbkdf2.Key(h, secret, salt, iter, len(key))
	if err != nil {
		return false, ErrMalformedHash
	}
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

func decodePBKDF2(encoded string) (h func() hash.Hash, iter int, salt, key []byte, err error) {
	// "", "PBKDF2-SHA512", "iterations=...", salt, hash
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "" {
		return nil, 0, nil, nil, ErrMalformedHash
	}
	switch parts[1] {
	case "PBKDF2-SHA512":
		h = sha512.New
	case "PBKDF2":
		h = sha1.New
	default:
		return nil, 0, nil, nil, ErrMalformedHash
	}
	n, ok := strings.CutPrefix(parts[2], "iterations=")
	if !ok {
		return nil, 0, nil, nil, ErrMalformedHash
	}
	iter, err = strconv.Atoi(n)
	if err != nil || iter < minIterations || iter > maxIterations {
		return nil, 0, nil, nil, ErrMalformedHash
	}
	salt, err1 := hex.DecodeString(parts[3])
	key, err2 := hex.DecodeString(parts[4])
	if err1 != nil || err2 != nil || len(salt) == 0 || len(key) < 16 || len(key) > 64 {
		return nil, 0, nil, nil, ErrMalformedHash
	}
	return h, iter, salt, key, nil
}
