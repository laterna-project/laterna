package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := b64.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example of RFC 8291, section 5: same keys, same salt, same message, so the same octets.
func TestEncryptRFC8291(t *testing.T) {
	sub := Subscription{
		Endpoint: "https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV",
		P256DH:   decode(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		Auth:     decode(t, "BTBZMqHH6r4Tts7J_aSIgg"),
	}
	if err := sub.Valid(); err != nil {
		t.Fatal(err)
	}
	sender, err := ecdh.P256().NewPrivateKey(decode(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(sender.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("sender public key: %s", got)
	}
	body, err := encrypt(sub, []byte("When I grow up, I want to be a watermelon"), sender, decode(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := b64.EncodeToString(body); got != want {
		t.Errorf("body of %d octets:\n%s\nwant\n%s", len(body), got, want)
	}

	if _, err := encrypt(sub, make([]byte, MaxPayload+1), sender, make([]byte, saltLen)); err == nil {
		t.Error("a payload over the limit was accepted")
	}
	// Two messages never share a key or a salt.
	a, err := Encrypt(sub, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encrypt(sub, []byte("same"))
	if string(a) == string(b) || len(a) != saltLen+4+1+publicKeyLen+len("same")+1+16 {
		t.Errorf("two encryptions: %d and %d octets, equal %v", len(a), len(b), string(a) == string(b))
	}
}

func TestSubscriptionValid(t *testing.T) {
	key := decode(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	auth := decode(t, "BTBZMqHH6r4Tts7J_aSIgg")
	offCurve := append([]byte{4}, make([]byte, 64)...)
	for name, s := range map[string]Subscription{
		"plain http":          {Endpoint: "http://push.example.net/x", P256DH: key, Auth: auth},
		"not an address":      {Endpoint: "push.example.net/x", P256DH: key, Auth: auth},
		"credentials":         {Endpoint: "https://user:pass@push.example.net/x", P256DH: key, Auth: auth},
		"endless address":     {Endpoint: "https://push.example.net/" + strings.Repeat("x", 2048), P256DH: key, Auth: auth},
		"short key":           {Endpoint: "https://push.example.net/x", P256DH: key[:32], Auth: auth},
		"point off the curve": {Endpoint: "https://push.example.net/x", P256DH: offCurve, Auth: auth},
		"short secret":        {Endpoint: "https://push.example.net/x", P256DH: key, Auth: auth[:8]},
	} {
		if s.Valid() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestKeys(t *testing.T) {
	k, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	if k.Zero() || !(Keys{}).Zero() {
		t.Error("Zero")
	}
	again, err := ParseKeys(k.String())
	if err != nil || again.PublicKey() != k.PublicKey() || again.String() != k.String() {
		t.Errorf("keys read back: %v", err)
	}
	if pub := decode(t, k.PublicKey()); len(pub) != publicKeyLen || pub[0] != 4 {
		t.Errorf("public key of %d octets", len(pub))
	}
	for _, bad := range []string{"", "not base64!", b64.EncodeToString(make([]byte, 32)), b64.EncodeToString([]byte("short"))} {
		if _, err := ParseKeys(bad); err == nil {
			t.Errorf("%q accepted as a key", bad)
		}
	}
}

// Send posts the encrypted message with what a push service asks for, and reads its answer.
func TestSend(t *testing.T) {
	keys, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	answer := http.StatusCreated
	var got *http.Request
	var size int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		size = int(r.ContentLength)
		if answer == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "30")
		}
		w.WriteHeader(answer)
		_, _ = w.Write([]byte("push service says so"))
	}))
	defer srv.Close()
	device, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{Endpoint: srv.URL + "/push/abc", P256DH: device.PublicKey().Bytes(), Auth: make([]byte, authLen)}
	c := &Client{HTTP: srv.Client(), Keys: keys, Subject: "https://laterna.example", Now: func() time.Time { return now }}

	if err := c.Send(context.Background(), sub, []byte("hello"), Options{TTL: 90 * time.Second, Topic: "t1"}); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("the push service received nothing")
	}
	if got.Method != http.MethodPost || got.URL.Path != "/push/abc" || got.Header.Get("Content-Encoding") != "aes128gcm" ||
		got.Header.Get("TTL") != "90" || got.Header.Get("Urgency") != "normal" || got.Header.Get("Topic") != "t1" ||
		size != saltLen+4+1+publicKeyLen+len("hello")+1+16 {
		t.Errorf("request: %v, %d octets", got.Header, size)
	}

	// The VAPID token: signed with the server's key, for this push service, for half a day.
	auth := got.Header.Get("Authorization")
	token, rest, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	if !ok || !strings.HasPrefix(auth, "vapid t=") || rest != keys.PublicKey() {
		t.Fatalf("authorization: %q", auth)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token: %q", token)
	}
	var header struct{ Typ, Alg string }
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	if err := errors.Join(json.Unmarshal(decode(t, parts[0]), &header), json.Unmarshal(decode(t, parts[1]), &claims)); err != nil {
		t.Fatal(err)
	}
	if header.Typ != "JWT" || header.Alg != "ES256" || claims.Aud != srv.URL || claims.Sub != "https://laterna.example" ||
		claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Errorf("token: %+v %+v", header, claims)
	}
	pub := decode(t, rest)
	sig := decode(t, parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 || !ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("the token is not signed with the server's key")
	}

	// What the push service answers.
	for status, check := range map[int]func(error) bool{
		http.StatusGone:     func(err error) bool { return errors.Is(err, ErrGone) },
		http.StatusNotFound: func(err error) bool { return errors.Is(err, ErrGone) },
		http.StatusTooManyRequests: func(err error) bool {
			var e *Error
			return errors.As(err, &e) && e.Status == 429 && e.RetryAfter == 30*time.Second && e.Message == "push service says so"
		},
		http.StatusForbidden: func(err error) bool {
			var e *Error
			return errors.As(err, &e) && e.Status == 403 && e.RetryAfter == 0
		},
	} {
		answer = status
		if err := c.Send(context.Background(), sub, []byte("hello"), Options{}); !check(err) {
			t.Errorf("HTTP %d: %v", status, err)
		}
	}
	if err := c.Send(context.Background(), Subscription{Endpoint: "http://push.example.net/x"}, nil, Options{}); err == nil {
		t.Error("an invalid subscription was sent to")
	}
}
