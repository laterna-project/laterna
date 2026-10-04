package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("172.16.0.0/12")}
	for name, c := range map[string]struct {
		remote string
		xff    []string
		want   string
	}{
		"no proxy":                       {remote: "203.0.113.5:4000", want: "203.0.113.5"},
		"header from a stranger ignored": {remote: "203.0.113.5:4000", xff: []string{"1.2.3.4"}, want: "203.0.113.5"},
		"behind the proxy":               {remote: "127.0.0.1:5000", xff: []string{"198.51.100.7"}, want: "198.51.100.7"},
		"made-up address on the left":    {remote: "127.0.0.1:5000", xff: []string{"6.6.6.6, 198.51.100.7"}, want: "198.51.100.7"},
		"deux proxys de confiance":       {remote: "127.0.0.1:5000", xff: []string{"198.51.100.7, 172.18.0.3"}, want: "198.51.100.7"},
		"several headers":                {remote: "127.0.0.1:5000", xff: []string{"198.51.100.7", "172.18.0.3"}, want: "198.51.100.7"},
		"IPv6":                           {remote: "127.0.0.1:5000", xff: []string{"2001:db8::1"}, want: "2001:db8::1"},
		"unreadable chain":               {remote: "127.0.0.1:5000", xff: []string{"198.51.100.7, n'importe quoi"}, want: "127.0.0.1"},
		"proxy without a header":         {remote: "127.0.0.1:5000", want: "127.0.0.1"},
		"IPv4 in trusted IPv6":           {remote: "[::ffff:127.0.0.1]:5000", xff: []string{"198.51.100.7"}, want: "198.51.100.7"},
	} {
		var got string
		h := ClientIP(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ClientIPFrom(r.Context()) }))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = c.remote
		for _, v := range c.xff {
			req.Header.Add("X-Forwarded-For", v)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}
