package domain

import (
	"encoding/json"
	"testing"
)

func TestNewIDFormat(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b || a.IsZero() {
		t.Fatal("two generated IDs must differ and be non-zero")
	}
	s := a.String()
	if len(s) != 36 || s[14] != '7' {
		t.Fatalf("want a canonical UUIDv7, got %q", s)
	}
	if a.String() > b.String() {
		t.Errorf("UUIDv7: lexical order must follow creation order (%s > %s)", a, b)
	}
}

func TestParseID(t *testing.T) {
	const compact = "0199a1b2c3d47e8f9a0b1c2d3e4f5061"
	const canonical = "0199a1b2-c3d4-7e8f-9a0b-1c2d3e4f5061"
	fromCompact, err := ParseID(compact)
	if err != nil {
		t.Fatal(err)
	}
	fromUpper, err := ParseID("0199A1B2-C3D4-7E8F-9A0B-1C2D3E4F5061")
	if err != nil {
		t.Fatal(err)
	}
	if fromCompact != fromUpper || fromCompact.String() != canonical {
		t.Errorf("compact=%s upper=%s", fromCompact, fromUpper)
	}
	for _, bad := range []string{"", "xyz", compact[:31], compact + "0", "0199a1b2c3d47e8f9a0b1c2d3e4f506g", "0199a1b2+c3d4-7e8f-9a0b-1c2d3e4f5061"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) should have failed", bad)
		}
	}
}

func TestIDJSON(t *testing.T) {
	id := NewID()
	raw, err := json.Marshal(map[string]ID{"Id": id})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"Id":"`+id.String()+`"}` {
		t.Errorf("unexpected JSON: %s", raw)
	}
	var back map[string]ID
	if err := json.Unmarshal(raw, &back); err != nil || back["Id"] != id {
		t.Errorf("JSON round trip: %v %v", back, err)
	}
}

func FuzzParseID(f *testing.F) {
	f.Add("0199a1b2c3d47e8f9a0b1c2d3e4f5061")
	f.Add("0199a1b2-c3d4-7e8f-9a0b-1c2d3e4f5061")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		id, err := ParseID(s)
		if err != nil {
			return
		}
		// Any ID we accept must round-trip through its canonical form.
		again, err := ParseID(id.String())
		if err != nil || again != id {
			t.Fatalf("round trip broken for %q", s)
		}
	})
}
