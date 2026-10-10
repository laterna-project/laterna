// Package webpush sends Web Push messages (RFC 8030) to the push service of a browser: the payload
// is encrypted for the device (RFC 8291, with the aes128gcm coding of RFC 8188), so the push service
// only sees where it goes, and the sender names itself with VAPID (RFC 8292). It has no dependency
// and knows nothing about the database.
package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

const (
	// MaxPayload is the longest payload a push service takes: 4096 octets less the header, the
	// padding delimiter and the authentication tag.
	MaxPayload = 3993
	// recordSize is the record size written in the header; a message is a single record.
	recordSize = 4096
	// publicKeyLen is the length of an uncompressed P-256 point, authLen of an authentication
	// secret.
	publicKeyLen = 65
	authLen      = 16
	saltLen      = 16
	// vapidLifetime is how long a VAPID token is valid; push services refuse more than a day.
	vapidLifetime = 12 * time.Hour
)

// Subscription is what a browser gives when it subscribes (PushSubscription): where to send, and
// the keys to encrypt for it.
type Subscription struct {
	// Endpoint is the address of the push service for this device.
	Endpoint string
	// P256DH is the public key of the device: an uncompressed P-256 point.
	P256DH []byte
	// Auth is its authentication secret.
	Auth []byte
}

// Valid checks the shape of a subscription: an https address without credentials, a key that is a
// point of the curve, a secret of the right length.
func (s Subscription) Valid() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(s.Endpoint) > 2048 {
		return errors.New("webpush: the endpoint is not an https address")
	}
	if _, err := ecdh.P256().NewPublicKey(s.P256DH); err != nil || len(s.P256DH) != publicKeyLen {
		return errors.New("webpush: the key is not a P-256 public key")
	}
	if len(s.Auth) != authLen {
		return errors.New("webpush: the authentication secret is not 16 octets long")
	}
	return nil
}

// Keys is the key pair a server names itself with (VAPID). A browser subscribes for one public key
// and its push service then only takes messages signed with it: the pair is created once and
// kept.
type Keys struct {
	private *ecdsa.PrivateKey
}

// NewKeys creates a key pair.
func NewKeys() (Keys, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return Keys{private: k}, err
}

// ParseKeys reads a key pair written by String.
func ParseKeys(s string) (Keys, error) {
	d, err := b64.DecodeString(s)
	if err != nil {
		return Keys{}, err
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return Keys{}, err
	}
	return Keys{private: k}, nil
}

// String writes the private key, from which the pair is rebuilt: a secret.
func (k Keys) String() string {
	d, err := k.private.Bytes()
	if err != nil {
		return ""
	}
	return b64.EncodeToString(d)
}

// PublicKey is the public key as browsers take it (applicationServerKey): the uncompressed point,
// in base64url.
func (k Keys) PublicKey() string {
	return b64.EncodeToString(k.publicBytes())
}

func (k Keys) publicBytes() []byte {
	out, err := k.private.PublicKey.Bytes()
	if err != nil {
		return nil
	}
	return out
}

// Zero reports keys that were never set.
func (k Keys) Zero() bool { return k.private == nil }

// Encrypt encrypts a payload for a device, with a fresh key and salt for each message.
func Encrypt(sub Subscription, payload []byte) ([]byte, error) {
	sender, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	return encrypt(sub, payload, sender, salt)
}

// encrypt is RFC 8291: a shared secret from the two keys and the authentication secret, then one
// aes128gcm record (RFC 8188) whose header carries the salt and the sender's public key.
func encrypt(sub Subscription, payload []byte, sender *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("webpush: payload of %d octets, %d at most", len(payload), MaxPayload)
	}
	device, err := ecdh.P256().NewPublicKey(sub.P256DH)
	if err != nil {
		return nil, err
	}
	shared, err := sender.ECDH(device)
	if err != nil {
		return nil, err
	}
	senderPublic := sender.PublicKey().Bytes()
	key, nonce, err := contentKeys(shared, sub.Auth, sub.P256DH, senderPublic, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 0, saltLen+4+1+len(senderPublic))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, publicKeyLen)
	header = append(header, senderPublic...)
	// 0x02 ends the last record; no padding after it.
	plain := append(append(make([]byte, 0, len(payload)+1), payload...), 2)
	return gcm.Seal(header, nonce, plain, nil), nil
}

// contentKeys derives the content encryption key and the nonce (RFC 8291, section 3.4).
func contentKeys(shared, auth, devicePublic, senderPublic, salt []byte) (key, nonce []byte, err error) {
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, nil, err
	}
	info := "WebPush: info\x00" + string(devicePublic) + string(senderPublic)
	ikm, err := hkdf.Expand(sha256.New, prkKey, info, 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if key, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	return key, nonce, err
}

// ErrGone is returned when the push service no longer knows the subscription: the browser
// unsubscribed or was uninstalled. It will never work again.
var ErrGone = errors.New("webpush: subscription gone")

// Error is a refusal from a push service.
type Error struct {
	Status int
	// RetryAfter is how long the push service asks to wait, 0 if it does not say.
	RetryAfter time.Duration
	Message    string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("webpush: HTTP %d", e.Status)
	}
	return fmt.Sprintf("webpush: HTTP %d: %s", e.Status, e.Message)
}

// Urgency says how much a message is worth waking a device for.
type Urgency string

// Urgencies a push service knows.
const (
	UrgencyLow    Urgency = "low"
	UrgencyNormal Urgency = "normal"
	UrgencyHigh   Urgency = "high"
)

// Options says how a message travels.
type Options struct {
	// TTL is how long the push service keeps the message for a device that is offline.
	TTL time.Duration
	// Urgency is UrgencyNormal when empty.
	Urgency Urgency
	// Topic replaces a waiting message with the same topic (32 URL-safe characters at most).
	Topic string
}

// Client sends messages.
type Client struct {
	// HTTP makes the requests; it should refuse redirects and private addresses, since endpoints
	// come from devices.
	HTTP *http.Client
	Keys Keys
	// Subject is who to contact about this server: an https or mailto address (VAPID "sub").
	Subject string
	// Now is the clock, time.Now when nil.
	Now func() time.Time
}

// Send encrypts a payload and hands it to the push service of a device.
func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte, o Options) error {
	if err := sub.Valid(); err != nil {
		return err
	}
	body, err := Encrypt(sub, payload)
	if err != nil {
		return err
	}
	auth, err := c.authorization(sub.Endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(o.TTL/time.Second)))
	req.Header.Set("Urgency", string(cmpOr(o.Urgency, UrgencyNormal)))
	if o.Topic != "" {
		req.Header.Set("Topic", o.Topic)
	}
	req.Header.Set("Authorization", auth)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	text, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		return ErrGone
	}
	e := &Error{Status: resp.StatusCode, Message: strings.TrimSpace(strings.ToValidUTF8(string(text), ""))}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	return e
}

func cmpOr(u, fallback Urgency) Urgency {
	if u == "" {
		return fallback
	}
	return u
}

// authorization is the VAPID header for an endpoint: a token signed with the server's key that
// names the push service it is meant for, and the public key.
func (c *Client) authorization(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	claims := map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(vapidLifetime).Unix()}
	if c.Subject != "" {
		claims["sub"] = c.Subject
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, c.Keys.private, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signed + "." + b64.EncodeToString(sig) + ", k=" + c.Keys.PublicKey(), nil
}
