package webauthn

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"math/big"
)

// COSE algorithms we accept (RFC 9053): those used by Apple, Google and Microsoft passkeys and by
// security keys.
const (
	algES256 = -7   // ECDSA P-256, SHA-256
	algEdDSA = -8   // Ed25519
	algRS256 = -257 // RSA PKCS#1 v1.5, SHA-256
)

var errKey = errors.New("authenticator public key unreadable or not accepted")

// publicKey is a COSE public key and its algorithm.
type publicKey struct {
	alg int64
	key crypto.PublicKey
}

// parseCOSEKey reads a COSE public key (a CBOR map) and checks that it is usable.
func parseCOSEKey(data []byte) (publicKey, error) {
	v, rest, err := decodeCBOR(data)
	if err != nil || len(rest) != 0 {
		return publicKey{}, errKey
	}
	m, ok := v.(map[any]any)
	if !ok {
		return publicKey{}, errKey
	}
	kty, _ := m[int64(1)].(int64)
	alg, _ := m[int64(3)].(int64)
	switch {
	case kty == 2 && alg == algES256:
		crv, _ := m[int64(-1)].(int64)
		x, _ := m[int64(-2)].([]byte)
		y, _ := m[int64(-3)].([]byte)
		if crv != 1 || len(x) != 32 || len(y) != 32 {
			return publicKey{}, errKey
		}
		key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return publicKey{}, errKey
		}
		return publicKey{alg: alg, key: key}, nil
	case kty == 1 && alg == algEdDSA:
		crv, _ := m[int64(-1)].(int64)
		x, _ := m[int64(-2)].([]byte)
		if crv != 6 || len(x) != ed25519.PublicKeySize {
			return publicKey{}, errKey
		}
		return publicKey{alg: alg, key: ed25519.PublicKey(x)}, nil
	case kty == 3 && alg == algRS256:
		n, _ := m[int64(-1)].([]byte)
		e, _ := m[int64(-2)].([]byte)
		if len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return publicKey{}, errKey // RSA under 2048 bits is rejected
		}
		exp := int(new(big.Int).SetBytes(e).Int64())
		if exp < 3 || exp%2 == 0 {
			return publicKey{}, errKey
		}
		return publicKey{alg: alg, key: &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}}, nil
	}
	return publicKey{}, errKey
}

// verify checks a signature made by the authenticator over data.
func (k publicKey) verify(data, sig []byte) bool {
	switch key := k.key.(type) {
	case *ecdsa.PublicKey:
		sum := sha256.Sum256(data)
		return ecdsa.VerifyASN1(key, sum[:], sig)
	case ed25519.PublicKey:
		return ed25519.Verify(key, data, sig)
	case *rsa.PublicKey:
		sum := sha256.Sum256(data)
		return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) == nil
	}
	return false
}
