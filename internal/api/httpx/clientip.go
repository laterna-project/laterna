package httpx

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

type clientIPKey struct{}

// ClientIP works out the client address of each request. Behind a trusted proxy (the connection
// comes from one), it is read from X-Forwarded-For: the first address from the right that is not
// itself a trusted proxy (addresses further left may have been made up by the client). Otherwise it
// is the connection's address and the header is ignored, since anyone could write it.
func ClientIP(trusted []netip.Prefix) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r, trusted)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip)))
		})
	}
}

// ClientIPFrom returns the client address set by ClientIP, or "".
func ClientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	return ip
}

func clientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	addr, err := netip.ParseAddr(peer)
	if err != nil || !trustedAddr(addr, trusted) {
		return peer
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for _, raw := range slices.Backward(hops) {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		hop, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			return peer // unreadable chain: stick to what is certain
		}
		if !trustedAddr(hop, trusted) {
			return hop.Unmap().String()
		}
	}
	return peer
}

func trustedAddr(a netip.Addr, trusted []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
