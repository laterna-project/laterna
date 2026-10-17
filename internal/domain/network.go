package domain

import "net/netip"

// IsLocal reports whether a client address is on the local network: in one of networks, or when
// none is given, in a private, loopback or link-local range. Playbacks from elsewhere are remote,
// and the server's bitrate limits apply to them. An address that cannot be read is remote.
func IsLocal(addr netip.Addr, networks []netip.Prefix) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if len(networks) == 0 {
		return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast()
	}
	for _, n := range networks {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}
