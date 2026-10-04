// Package auth has the cryptographic building blocks for authentication: password and PIN hashing
// (argon2id), session tokens, attempt limiting. It knows nothing about the database or HTTP.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// params tunes argon2id. These are the OWASP recommendations (2025): 19 MiB, 2 passes, 1 thread.
// Reasonable on a NAS too.
type params struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
	keyLen  uint32
	saltLen int
}

var current = params{memory: 19 * 1024, time: 2, threads: 1, keyLen: 32, saltLen: 16}

var b64 = base64.RawStdEncoding

// ErrMalformedHash is returned for a stored hash that cannot be read.
var ErrMalformedHash = errors.New("unreadable password hash")

// HashPassword returns the argon2id hash of a secret in PHC format:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>.
func HashPassword(secret string) (string, error) {
	salt := make([]byte, current.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := idKey([]byte(secret), salt, current.time, current.memory, current.threads, current.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, current.memory, current.time, current.threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword compares a secret to a hash in constant time. needsRehash reports a hash made with
// weaker parameters than the current ones, or imported from another server (legacy.go): it should
// be recomputed while we have the secret in clear.
func VerifyPassword(encoded, secret string) (ok, needsRehash bool, err error) {
	if strings.HasPrefix(encoded, "$PBKDF2") { // imported from Jellyfin: always rehash
		ok, err := verifyPBKDF2(encoded, secret)
		return ok, ok, err
	}
	p, salt, key, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	//nolint:gosec // keyLen comes from a hash we produced (32 bytes)
	got := idKey([]byte(secret), salt, p.time, p.memory, p.threads, uint32(len(key)))
	if subtle.ConstantTimeCompare(got, key) != 1 {
		return false, false, nil
	}
	return true, p.memory < current.memory || p.time < current.time, nil
}

func decode(encoded string) (params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return params{}, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return params{}, nil, nil, ErrMalformedHash
	}
	var p params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return params{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}

// idKey runs argon2id, then gives the memory back to the OS. Each run allocates all its memory (19
// MiB) in one block that is useless right after. On an idle server Go would only free it at the
// forced GC two minutes later, then return it to the OS bit by bit, so the server would sit on 20
// MB too many for minutes after each login. A GC on a small heap costs about 1 ms, outside the
// request, next to the 20 ms argon2id takes.
//
// At most two runs at a time (argonSlots): a hundred logins sent at once would otherwise take close
// to 2 GB. The rest wait for their turn.
func idKey(secret, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	argonSlots <- struct{}{}
	key := argon2.IDKey(secret, salt, time, memory, threads, keyLen)
	<-argonSlots
	go debug.FreeOSMemory()
	return key
}

// argonSlots caps concurrent argon2id runs (19 MiB each).
var argonSlots = make(chan struct{}, 2)

// dummyHash is checked against when the account does not exist. It takes the same time, so response
// time does not leak whether an account exists. It is computed on the first login for an unknown
// account rather than at startup: one argon2id run costs 20 ms and 19 MiB.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword("laterna-compte-inexistant")
	if err != nil {
		panic(err)
	}
	return h
})

// BurnTime runs a fake verification that takes as long as a real one.
func BurnTime(secret string) {
	_, _, _ = VerifyPassword(dummyHash(), secret)
}
