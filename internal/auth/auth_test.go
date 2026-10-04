package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("A very safe password")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected format: %s", hash)
	}
	if ok, rehash, err := VerifyPassword(hash, "A very safe password"); !ok || rehash || err != nil {
		t.Errorf("right password rejected: %v %v %v", ok, rehash, err)
	}
	if ok, _, err := VerifyPassword(hash, "a very safe password"); ok || err != nil {
		t.Errorf("wrong password accepted: %v %v", ok, err)
	}
	other, _ := HashPassword("A very safe password")
	if other == hash {
		t.Error("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyFlagsWeakParameters(t *testing.T) {
	weak := current
	weak.memory = 8 * 1024
	saved := current
	current = weak
	hash, err := HashPassword("secret")
	current = saved
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash, err := VerifyPassword(hash, "secret")
	if !ok || !rehash || err != nil {
		t.Errorf("weak hash: ok=%v rehash=%v err=%v, want a rehash", ok, rehash, err)
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2Fs$aGFzaA", "$argon2id$v=18$m=1,t=1,p=1$c2Fs$aGFzaA", "$argon2id$v=19$m=x$c2Fs$aGFzaA", "$argon2id$v=19$m=1,t=1,p=1$!!$aGFzaA"} {
		if _, _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("hash %q accepted", bad)
		}
	}
}

func FuzzVerifyPassword(f *testing.F) {
	f.Add("$argon2id$v=19$m=19456,t=2,p=1$c2Fs$aGFzaA", "x")
	f.Fuzz(func(_ *testing.T, encoded, secret string) {
		// Must never panic, whatever the stored hash is. The requested cost is capped to keep
		// fuzzing fast.
		p, _, _, err := decode(encoded)
		if err != nil || p.memory > 64 || p.time > 2 || p.threads == 0 {
			return
		}
		_, _, _ = VerifyPassword(encoded, secret)
	})
}

func TestTokens(t *testing.T) {
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) || len(token) != len(TokenPrefix)+43 {
		t.Errorf("unexpected token: %q", token)
	}
	if HashToken(token) != hash || len(hash) != 64 {
		t.Errorf("inconsistent hash: %q", hash)
	}
	other, _, _ := NewToken()
	if other == token {
		t.Error("two identical tokens")
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer lat_abc", "lat_abc", true},
		{"bearer   lat_abc  ", "lat_abc", true},
		{"Basic lat_abc", "", false},
		{"Bearer autre", "autre", false},
		{"", "", false},
		{"lat_abc", "", false},
	}
	for _, tt := range tests {
		got, ok := BearerToken(tt.header)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("BearerToken(%q) = %q, %v", tt.header, got, ok)
		}
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	l := NewLimiter(3, 10*time.Minute)
	l.now = func() time.Time { return now }

	for range 3 {
		if _, ok := l.Allow("account:alice", "ip:1.2.3.4"); !ok {
			t.Fatal("blocked too early")
		}
		l.Fail("account:alice", "ip:1.2.3.4")
	}
	wait, ok := l.Allow("account:alice")
	if ok || wait != 10*time.Minute {
		t.Fatalf("after 3 failures: ok=%v wait=%v", ok, wait)
	}
	if _, ok := l.Allow("compte:autre"); !ok {
		t.Error("another key must not be blocked")
	}

	now = now.Add(11 * time.Minute)
	if _, ok := l.Allow("account:alice"); !ok {
		t.Error("the window elapsing must unblock")
	}

	l.Fail("ip:5.6.7.8")
	l.Succeed("ip:5.6.7.8")
	if _, found := l.entries["ip:5.6.7.8"]; found {
		t.Error("a success must clear the failures")
	}
}
