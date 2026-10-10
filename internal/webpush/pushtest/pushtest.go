// Package pushtest fakes the push service of a browser for tests: devices subscribe to it, and it
// takes the messages sent to them the way a real one does (TLS, VAPID token checked), then decrypts
// them as the device would.
package pushtest

import (
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
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/laterna-project/laterna/internal/webpush"
)

var b64 = base64.RawURLEncoding

// Service is a fake push service.
type Service struct {
	*httptest.Server

	mu      sync.Mutex
	devices map[string]*Device
	// Refused counts the messages refused for a bad token or a bad shape.
	refused int
}

// Device is a browser subscribed to the service.
type Device struct {
	// Subscription is what the browser would hand to the server.
	Subscription webpush.Subscription
	private      *ecdh.PrivateKey
	gone         bool
	messages     [][]byte
	ttls         []int
}

// New starts a fake push service that is stopped when the test ends. Its client (Client) trusts
// its certificate.
func New(t *testing.T) *Service {
	t.Helper()
	s := &Service{devices: map[string]*Device{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// NewDevice subscribes a browser.
func (s *Service) NewDevice(t *testing.T) *Device {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, auth); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := "/push/" + strconv.Itoa(len(s.devices)+1)
	d := &Device{private: key, Subscription: webpush.Subscription{Endpoint: s.URL + path, P256DH: key.PublicKey().Bytes(), Auth: auth}}
	s.devices[path] = d
	return d
}

// Messages returns the payloads a device received, decrypted, in order.
func (s *Service) Messages(d *Device) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(d.messages))
	for i, m := range d.messages {
		out[i] = string(m)
	}
	return out
}

// TTLs returns the TTL, in seconds, of each message a device received.
func (s *Service) TTLs(d *Device) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), d.ttls...)
}

// Unsubscribe makes the service forget a device: it then answers that the subscription is gone.
func (s *Service) Unsubscribe(d *Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d.gone = true
}

// Refused counts the messages the service refused (bad token, bad shape).
func (s *Service) Refused() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[r.URL.Path]
	if d == nil || d.gone {
		w.WriteHeader(http.StatusGone)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	ttl, ttlErr := strconv.Atoi(r.Header.Get("TTL"))
	if err != nil || ttlErr != nil || r.Method != http.MethodPost || r.Header.Get("Content-Encoding") != "aes128gcm" ||
		len(body) > 4096 || s.checkToken(r.Header.Get("Authorization")) != nil {
		s.refused++
		w.WriteHeader(http.StatusForbidden)
		return
	}
	plain, err := decrypt(body, d.private, d.Subscription.Auth)
	if err != nil {
		s.refused++
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	d.messages, d.ttls = append(d.messages, plain), append(d.ttls, ttl)
	w.WriteHeader(http.StatusCreated)
}

// checkToken checks a VAPID header: a token signed with the key it names, meant for this service,
// not expired and valid for a day at most.
func (s *Service) checkToken(header string) error {
	token, key, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	parts := strings.Split(token, ".")
	if !ok || !strings.HasPrefix(header, "vapid t=") || len(parts) != 3 {
		return errors.New("not a VAPID header")
	}
	pub, err := b64.DecodeString(key)
	if err != nil || len(pub) != 65 {
		return errors.New("bad key")
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return errors.New("bad signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	k, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		return err
	}
	if !ecdsa.Verify(k, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return errors.New("signature does not match")
	}
	raw, err := b64.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return err
	}
	left := time.Until(time.Unix(claims.Exp, 0))
	if claims.Aud != s.URL || claims.Sub == "" || left > 24*time.Hour {
		return errors.New("claims refused")
	}
	return nil
}

// decrypt is the device's side of RFC 8291.
func decrypt(body []byte, device *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < 21 || len(body) < 21+int(body[20]) {
		return nil, errors.New("short message")
	}
	salt, size, idLen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	senderPublic, record := body[21:21+idLen], body[21+idLen:]
	if size != 4096 {
		return nil, errors.New("unexpected record size")
	}
	sender, err := ecdh.P256().NewPublicKey(senderPublic)
	if err != nil {
		return nil, err
	}
	shared, err := device.ECDH(sender)
	if err != nil {
		return nil, err
	}
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(device.PublicKey().Bytes())+string(senderPublic), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
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
	plain, err := gcm.Open(nil, nonce, record, nil)
	if err != nil {
		return nil, err
	}
	// The last record ends with 0x02, then optional zeros.
	plain = []byte(strings.TrimRight(string(plain), "\x00"))
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, errors.New("missing record delimiter")
	}
	return plain[:len(plain)-1], nil
}
