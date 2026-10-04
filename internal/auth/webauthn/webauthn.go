// Package webauthn verifies passkeys (WebAuthn level 3) without any dependency: registration and
// login options, then verification of what the authenticator signed. It stores nothing; challenges
// and keys are kept by the caller.
//
// Only what protects the account is checked: the challenge, the origin, the hash of the relying
// party ID, user presence and verification, the signature and its counter. Attestation (the
// authenticator's make) is requested as "none" and ignored: its only use is to restrict which
// models are accepted.
package webauthn

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Timeout is how long the user has, and also the lifetime of a challenge.
const Timeout = 5 * time.Minute

// RP is the site the passkeys are for: its ID (a domain name, that of the origins or one of their
// parents) and the origins requests may come from (a web page, an Android app as
// "android:apk-key-hash:...").
type RP struct {
	ID      string
	Name    string
	Origins []string
}

// User is the account of a passkey. Handle is its opaque ID, which the authenticator gives back at
// login.
type User struct {
	Handle            []byte
	Name, DisplayName string
}

// Credential is a registered passkey: its ID, its COSE public key and its signature counter.
type Credential struct {
	ID        []byte
	PublicKey []byte
	SignCount uint32
}

// ErrInvalid is returned for a rejected authenticator response. The message says why.
var ErrInvalid = errors.New("passkey rejected")

func invalid(why string) error { return errors.Join(ErrInvalid, errors.New(why)) }

// NewChallenge returns a random challenge.
func NewChallenge() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never returns an error
	return b
}

// Encode writes bytes as unpadded base64url, the form used in WebAuthn JSON.
func Encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

type descriptor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// CreationOptions returns the options to register a passkey, in the JSON form read by
// PublicKeyCredential.parseCreationOptionsFromJSON (and by the Android and iOS APIs): discoverable
// key (login without a username), user verified, no attestation. exclude lists the passkeys the
// account already has, so the authenticator does not register twice.
func CreationOptions(rp RP, u User, challenge []byte, exclude [][]byte) []byte {
	type param struct {
		Type string `json:"type"`
		Alg  int    `json:"alg"`
	}
	opts := struct {
		RP struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"rp"`
		User struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
		Challenge          string       `json:"challenge"`
		Params             []param      `json:"pubKeyCredParams"`
		Timeout            int64        `json:"timeout"`
		Exclude            []descriptor `json:"excludeCredentials"`
		AuthenticatorSelec struct {
			ResidentKey        string `json:"residentKey"`
			RequireResidentKey bool   `json:"requireResidentKey"`
			UserVerification   string `json:"userVerification"`
		} `json:"authenticatorSelection"`
		Attestation string `json:"attestation"`
	}{
		Challenge: Encode(challenge), Timeout: Timeout.Milliseconds(), Exclude: []descriptor{}, Attestation: "none",
		Params: []param{{"public-key", algES256}, {"public-key", algEdDSA}, {"public-key", algRS256}},
	}
	opts.RP.ID, opts.RP.Name = rp.ID, rp.Name
	opts.User.ID, opts.User.Name, opts.User.DisplayName = Encode(u.Handle), u.Name, u.DisplayName
	opts.AuthenticatorSelec.ResidentKey, opts.AuthenticatorSelec.RequireResidentKey = "required", true
	opts.AuthenticatorSelec.UserVerification = "required"
	for _, id := range exclude {
		opts.Exclude = append(opts.Exclude, descriptor{Type: "public-key", ID: Encode(id)})
	}
	b, _ := json.Marshal(opts) // strings and numbers: cannot fail
	return b
}

// RequestOptions returns the login options (PublicKeyCredential.parseRequestOptionsFromJSON): any
// passkey of the site, user verified.
func RequestOptions(rp RP, challenge []byte) []byte {
	b, _ := json.Marshal(struct {
		Challenge        string       `json:"challenge"`
		RPID             string       `json:"rpId"`
		Timeout          int64        `json:"timeout"`
		UserVerification string       `json:"userVerification"`
		Allow            []descriptor `json:"allowCredentials"`
	}{Encode(challenge), rp.ID, Timeout.Milliseconds(), "required", []descriptor{}})
	return b
}

// credentialJSON is an authenticator response (PublicKeyCredential.toJSON()).
type credentialJSON struct {
	ID       string `json:"id"`
	RawID    string `json:"rawId"`
	Type     string `json:"type"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AttestationObject string `json:"attestationObject"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
		UserHandle        string `json:"userHandle"`
	} `json:"response"`
}

func parseCredential(data []byte) (credentialJSON, []byte, error) {
	var c credentialJSON
	if err := json.Unmarshal(data, &c); err != nil || c.Type != "public-key" {
		return c, nil, invalid("unreadable response")
	}
	id, err := decode(c.RawID)
	if err != nil || len(id) == 0 || len(id) > 1023 {
		return c, nil, invalid("unreadable passkey ID")
	}
	return c, id, nil
}

// clientData checks the client data: operation type, challenge, origin.
func clientData(rp RP, raw string, kind string, challenge []byte) ([]byte, error) {
	data, err := decode(raw)
	if err != nil {
		return nil, invalid("unreadable client data")
	}
	var c struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, invalid("unreadable client data")
	}
	got, err := decode(c.Challenge)
	switch {
	case c.Type != kind:
		return nil, invalid("unexpected operation")
	case err != nil || subtle.ConstantTimeCompare(got, challenge) != 1:
		return nil, invalid("unexpected challenge")
	case !slices.Contains(rp.Origins, c.Origin):
		return nil, invalid("origin not allowed: " + c.Origin)
	case c.CrossOrigin:
		return nil, invalid("request from a cross-origin frame")
	}
	return data, nil
}

const (
	flagUserPresent  = 0x01
	flagUserVerified = 0x04
	flagAttested     = 0x40
	flagExtensions   = 0x80
)

// authData reads the authenticator data: RP ID hash, flags, counter and, at registration, the
// passkey's ID and key.
type authData struct {
	flags     byte
	signCount uint32
	credID    []byte
	key       []byte
}

func parseAuthData(rp RP, b []byte, attested bool) (authData, error) {
	if len(b) < 37 {
		return authData{}, invalid("authenticator data too short")
	}
	want := sha256.Sum256([]byte(rp.ID))
	if subtle.ConstantTimeCompare(b[:32], want[:]) != 1 {
		return authData{}, invalid("passkey of another site")
	}
	a := authData{flags: b[32], signCount: binary.BigEndian.Uint32(b[33:37])}
	switch {
	case a.flags&flagUserPresent == 0:
		return a, invalid("user not present")
	case a.flags&flagUserVerified == 0:
		return a, invalid("user not verified (PIN, fingerprint or face)")
	case attested != (a.flags&flagAttested != 0):
		return a, invalid("unexpected authenticator data")
	}
	rest := b[37:]
	if attested {
		if len(rest) < 18 {
			return a, invalid("passkey data too short")
		}
		n := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]
		if n == 0 || n > 1023 || len(rest) < n {
			return a, invalid("unreadable passkey ID")
		}
		a.credID, rest = rest[:n], rest[n:]
		_, after, err := decodeCBOR(rest)
		if err != nil {
			return a, invalid("unreadable passkey key")
		}
		a.key, rest = rest[:len(rest)-len(after)], after
	}
	if len(rest) > 0 && a.flags&flagExtensions == 0 {
		return a, invalid("trailing authenticator data")
	}
	return a, nil
}

// VerifyRegistration checks the registration of a passkey (the response of
// navigator.credentials.create to challenge) and returns what to store.
func VerifyRegistration(rp RP, challenge, response []byte) (Credential, error) {
	c, id, err := parseCredential(response)
	if err != nil {
		return Credential{}, err
	}
	if _, err := clientData(rp, c.Response.ClientDataJSON, "webauthn.create", challenge); err != nil {
		return Credential{}, err
	}
	raw, err := decode(c.Response.AttestationObject)
	if err != nil {
		return Credential{}, invalid("unreadable attestation")
	}
	v, rest, err := decodeCBOR(raw)
	m, ok := v.(map[any]any)
	if err != nil || len(rest) != 0 || !ok {
		return Credential{}, invalid("unreadable attestation")
	}
	data, _ := m["authData"].([]byte)
	a, err := parseAuthData(rp, data, true)
	if err != nil {
		return Credential{}, err
	}
	if !bytes.Equal(a.credID, id) {
		return Credential{}, invalid("inconsistent passkey ID")
	}
	if _, err := parseCOSEKey(a.key); err != nil {
		return Credential{}, invalid(err.Error())
	}
	return Credential{ID: id, PublicKey: a.key, SignCount: a.signCount}, nil
}

// CredentialID returns the passkey ID of a login response and the opaque ID of its account
// (userHandle), so the passkey can be looked up before it is verified.
func CredentialID(response []byte) (id, userHandle []byte, err error) {
	c, id, err := parseCredential(response)
	if err != nil {
		return nil, nil, err
	}
	if c.Response.UserHandle != "" {
		if userHandle, err = decode(c.Response.UserHandle); err != nil {
			return nil, nil, invalid("unreadable account ID")
		}
	}
	return id, userHandle, nil
}

// VerifyAssertion checks a login (the response of navigator.credentials.get to challenge) against
// passkey cred and returns its new counter. A counter that does not move forward gives away a
// cloned passkey. A synced passkey has no counter: always 0.
func VerifyAssertion(rp RP, challenge []byte, cred Credential, response []byte) (uint32, error) {
	c, id, err := parseCredential(response)
	if err != nil {
		return 0, err
	}
	if !bytes.Equal(id, cred.ID) {
		return 0, invalid("another passkey")
	}
	clientJSON, err := clientData(rp, c.Response.ClientDataJSON, "webauthn.get", challenge)
	if err != nil {
		return 0, err
	}
	data, err := decode(c.Response.AuthenticatorData)
	if err != nil {
		return 0, invalid("unreadable authenticator data")
	}
	a, err := parseAuthData(rp, data, false)
	if err != nil {
		return 0, err
	}
	sig, err := decode(c.Response.Signature)
	if err != nil {
		return 0, invalid("unreadable signature")
	}
	key, err := parseCOSEKey(cred.PublicKey)
	if err != nil {
		return 0, invalid(err.Error())
	}
	hash := sha256.Sum256(clientJSON)
	if !key.verify(append(append([]byte(nil), data...), hash[:]...), sig) {
		return 0, invalid("wrong signature")
	}
	if (a.signCount != 0 || cred.SignCount != 0) && a.signCount <= cred.SignCount {
		return 0, invalid("passkey counter went backwards: cloned passkey?")
	}
	return a.signCount, nil
}
