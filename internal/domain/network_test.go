package domain

import (
	"net/netip"
	"testing"
)

func TestIsLocal(t *testing.T) {
	home := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("fd00::/8")}
	for _, c := range []struct {
		addr     string
		networks []netip.Prefix
		local    bool
	}{
		{"192.168.1.20", nil, true},
		{"10.0.0.5", nil, true},
		{"172.20.1.1", nil, true},
		{"127.0.0.1", nil, true},
		{"::1", nil, true},
		{"fe80::1", nil, true},
		{"fd12:3456::1", nil, true},
		{"::ffff:192.168.1.20", nil, true},
		{"203.0.113.7", nil, false},
		{"2001:db8::1", nil, false},
		// CGNAT, as some VPNs use: remote unless listed.
		{"100.64.0.1", nil, false},
		// Only the listed networks are local: a VPN in another private range is not.
		{"192.168.1.20", home, true},
		{"10.8.0.2", home, false},
		{"fd00::5", home, true},
		{"::ffff:192.168.1.20", home, true},
	} {
		if got := IsLocal(netip.MustParseAddr(c.addr), c.networks); got != c.local {
			t.Errorf("IsLocal(%s, %v) = %v, want %v", c.addr, c.networks, got, c.local)
		}
	}
	if IsLocal(netip.Addr{}, nil) {
		t.Error("an unknown address is local")
	}
}
