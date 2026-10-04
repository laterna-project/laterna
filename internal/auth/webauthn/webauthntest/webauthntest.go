// Package webauthntest is a software authenticator for tests. It registers an ES256 passkey and
// signs logins the way a phone would, with the same structures (CBOR, client data as JSON).
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Authenticator is a passkey and the device that holds it.
type Authenticator struct {
	// Origin announced in the client data (the page or the app).
	Origin string
	// Count is the signature counter (0 for a synced passkey, which has none).
	Count uint32
	// Verified means the user authenticated on the device (PIN, fingerprint).
	Verified bool

	key        *ecdsa.PrivateKey
	credID     []byte
	userHandle []byte
	rpID       string
}

// New creates an authenticator for an origin.
func New(origin string) *Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &Authenticator{Origin: origin, Verified: true, key: key, credID: id}
}

// ID returns the passkey ID.
func (a *Authenticator) ID() []byte { return a.credID }

var b64 = base64.RawURLEncoding

// Create answers registration options (the JSON from CreationOptions).
func (a *Authenticator) Create(options []byte) ([]byte, error) {
	var o struct {
		RP struct {
			ID string `json:"id"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, err
	}
	handle, err := b64.DecodeString(o.User.ID)
	if err != nil {
		return nil, err
	}
	a.rpID, a.userHandle = o.RP.ID, handle
	x, y := make([]byte, 32), make([]byte, 32)
	pub, err := a.key.PublicKey.Bytes() // 04 || X || Y
	if err != nil {
		return nil, err
	}
	copy(x, pub[1:33])
	copy(y, pub[33:65])
	// COSE key: {1: 2 (EC2), 3: -7 (ES256), -1: 1 (P-256), -2: x, -3: y}.
	key := cborMap(5)
	key = append(key, cborInt(1)...)
	key = append(key, cborInt(2)...)
	key = append(key, cborInt(3)...)
	key = append(key, cborInt(-7)...)
	key = append(key, cborInt(-1)...)
	key = append(key, cborInt(1)...)
	key = append(key, cborInt(-2)...)
	key = append(key, cborBytes(x)...)
	key = append(key, cborInt(-3)...)
	key = append(key, cborBytes(y)...)
	data := a.authData(0x40)
	data = append(data, make([]byte, 16)...)                          // AAGUID
	data = binary.BigEndian.AppendUint16(data, uint16(len(a.credID))) //nolint:gosec // 16 bytes
	data = append(data, a.credID...)
	data = append(data, key...)
	att := cborMap(3)
	att = append(att, cborText("fmt")...)
	att = append(att, cborText("none")...)
	att = append(att, cborText("attStmt")...)
	att = append(att, cborMap(0)...)
	att = append(att, cborText("authData")...)
	att = append(att, cborBytes(data)...)
	client := a.clientData("webauthn.create", o.Challenge)
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]string{"clientDataJSON": b64.EncodeToString(client), "attestationObject": b64.EncodeToString(att)},
	})
}

// Get answers login options (the JSON from RequestOptions).
func (a *Authenticator) Get(options []byte) ([]byte, error) {
	var o struct {
		RPID      string `json:"rpId"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(options, &o); err != nil {
		return nil, err
	}
	if a.userHandle == nil {
		return nil, errors.New("passkey never registered")
	}
	if o.RPID != a.rpID {
		return nil, fmt.Errorf("no passkey for %s", o.RPID)
	}
	if a.Count > 0 {
		a.Count++
	}
	data := a.authData(0)
	client := a.clientData("webauthn.get", o.Challenge)
	hash := sha256.Sum256(client)
	sum := sha256.Sum256(append(append([]byte(nil), data...), hash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, sum[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]string{
			"clientDataJSON": b64.EncodeToString(client), "authenticatorData": b64.EncodeToString(data),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(a.userHandle),
		},
	})
}

func (a *Authenticator) authData(flags byte) []byte {
	rp := sha256.Sum256([]byte(a.rpID))
	flags |= 0x01 // user present
	if a.Verified {
		flags |= 0x04
	}
	data := append(rp[:], flags)
	return binary.BigEndian.AppendUint32(data, a.Count)
}

func (a *Authenticator) clientData(kind, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
	return b
}

// A CBOR writer cut down to what the authenticator needs.

func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 1<<8:
		return []byte{major<<5 | 24, byte(n)}
	default:
		return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n)) //nolint:gosec // test sizes
	}
}

func cborInt(v int64) []byte {
	if v < 0 {
		return cborHead(1, uint64(-1-v))
	}
	return cborHead(0, uint64(v))
}

func cborBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }
func cborText(s string) []byte  { return append(cborHead(3, uint64(len(s))), s...) }
func cborMap(n int) []byte      { return cborHead(5, uint64(n)) } //nolint:gosec // n >= 0
