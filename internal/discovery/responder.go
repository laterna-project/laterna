package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	// mdnsPort is the mDNS port and group its IPv4 group address.
	mdnsPort = 5353
	// rescan is the time between interface checks. An interface that shows up (Wi-Fi back, DHCP)
	// joins the group and gets the announcement.
	rescan = time.Minute
	// spacing is the minimum delay before the same record is sent to the group again on the same
	// interface (RFC 6762, section 6). A burst of queries does not make a burst of answers.
	spacing = time.Second
	// recheck is the minimum time between interface checks triggered by a query from an unknown
	// network. An interface may just have appeared, but a stranger must not make us list the
	// interfaces on every packet.
	recheck = 5 * time.Second
)

var group = netip.MustParseAddr("224.0.0.251")

// Options configures the responder.
type Options struct {
	// Port is the server's HTTP port.
	Port int
	// Only is the single address announced when the server listens on nothing else. Invalid means
	// the addresses of the interface each query comes in on.
	Only netip.Addr
	// Info is called for each answer.
	Info   func() Info
	Logger *slog.Logger

	// listen is the group listen address; empty means 224.0.0.251:5353. Tests take a free port and
	// query it directly.
	listen string
}

// Responder answers mDNS queries looking for the server.
type Responder struct {
	opts   Options
	log    *slog.Logger
	conn   *net.UDPConn
	pc     *ipv4.PacketConn
	port   uint16 // listen port, the group's
	ifaces bool   // the incoming interface of each packet is known (not on Windows)

	mu      sync.Mutex // one write at a time: the outgoing interface is set before each one
	links   []link
	joined  map[int]bool
	last    map[sent]time.Time
	checked time.Time // last interface check triggered by a query from an unknown network
}

// Listen opens the mDNS port (shared with avahi or mDNSResponder if they run) and joins the group
// on every active interface.
func Listen(opts Options) (*Responder, error) {
	addr := opts.listen
	if addr == "" {
		addr = netip.AddrPortFrom(group, mdnsPort).String()
	}
	gaddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	// ListenMulticastUDP makes the port shareable on every OS (SO_REUSEADDR, and SO_REUSEPORT where
	// needed) and listens on all addresses.
	conn, err := net.ListenMulticastUDP("udp4", nil, gaddr)
	if err != nil {
		return nil, fmt.Errorf("discovery: listening on %s: %w", addr, err)
	}
	r := &Responder{
		opts: opts, log: opts.Logger, conn: conn, pc: ipv4.NewPacketConn(conn),
		joined: map[int]bool{}, last: map[sent]time.Time{},
	}
	if local, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		r.port = local.AddrPort().Port()
	}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	r.ifaces = r.pc.SetControlMessage(ipv4.FlagInterface, true) == nil
	// A client on the same machine (avahi-browse) hears the answers too.
	_ = r.pc.SetMulticastLoopback(true)
	_ = r.pc.SetMulticastTTL(255) // RFC 6762, section 11
	r.refresh()
	return r, nil
}

// Addresses returns the addresses the server announces: those of its active interfaces, or the only
// one it listens on.
func (r *Responder) Addresses() []netip.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	return pick(r.links, 0, netip.Addr{}, r.opts.Only)
}

// Port returns the UDP listen port.
func (r *Responder) Port() int { return int(r.port) }

// Serve answers until ctx is canceled, then says goodbye and closes the port.
func (r *Responder) Serve(ctx context.Context) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		r.announce(ctx, nil)
		tick := time.NewTicker(rescan)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				r.goodbye()
				_ = r.conn.Close()
				return
			case <-tick.C:
				r.announce(ctx, r.refresh())
			}
		}
	}()
	buf := make([]byte, 9000)
	for {
		n, cm, src, err := r.pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("discovery: reading: %w", err)
		}
		from, ok := src.(*net.UDPAddr)
		if !ok {
			continue
		}
		index := 0
		if cm != nil {
			index = cm.IfIndex
		}
		r.handle(buf[:n], from.AddrPort(), index)
	}
}

// handle answers a query from src that came in on interface index (0 if unknown).
func (r *Responder) handle(query []byte, src netip.AddrPort, index int) {
	resp, recs, index, legacy := r.reply(query, src, index)
	if resp == nil {
		return
	}
	if legacy {
		r.mu.Lock()
		defer r.mu.Unlock()
		if _, err := r.pc.WriteTo(resp, nil, net.UDPAddrFromAddrPort(src)); err != nil {
			r.log.Debug("discovery: answering a unicast query", "to", src, "err", err)
		}
		return
	}
	r.multicast(resp, index, recs, false)
}

// reply builds the answer to a query from src that came in on interface index (0 if unknown); nil
// if it does not come from the local network or asks for nothing in the zone. It also returns the
// records of the answer, the interface to answer on, and whether the query is a plain DNS query
// (unicast answer).
func (r *Responder) reply(query []byte, src netip.AddrPort, index int) (resp []byte, recs []record, out int, legacy bool) {
	addr := src.Addr().Unmap()
	links, ok := r.linksFor(addr)
	if !ok {
		return nil, nil, 0, false
	}
	if index == 0 {
		index = linkOf(links, addr)
	}
	z, err := newZone(r.opts.Info(), r.opts.Port, pick(links, index, addr, r.opts.Only))
	if err != nil {
		return nil, nil, 0, false
	}
	legacy = src.Port() != mdnsPort
	resp, recs = answer(query, z, legacy)
	return resp, recs, index, legacy
}

// linksFor returns the active interfaces if addr is on the local network (see local). Otherwise it
// returns false and the query is ignored. An unknown address triggers an interface check, at most
// once every five seconds: maybe the Wi-Fi just came back or the lease changed.
func (r *Responder) linksFor(addr netip.Addr) ([]link, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if local(r.links, addr) {
		return r.links, true
	}
	if now := time.Now(); now.Sub(r.checked) >= recheck {
		r.checked = now
		r.links = activeLinks()
		return r.links, local(r.links, addr)
	}
	return nil, false
}

// sent identifies a record sent to the group through an interface.
type sent struct {
	index int
	rec   record
}

// due reports whether an answer carrying recs should go out through interface index, and if so
// records that it did. A record is not sent to the group again less than a second after it went out
// (RFC 6762, section 6): the answer is held back if all its records just left, since whoever asks
// has heard them, but a different answer goes out without waiting. force skips the check
// (announcements). r.mu must be held.
func (r *Responder) due(index int, recs []record, now time.Time, force bool) bool {
	fresh := force
	for _, rec := range recs {
		if now.Sub(r.last[sent{index, rec}]) >= spacing {
			fresh = true
		}
	}
	if !fresh {
		return false
	}
	for _, rec := range recs {
		r.last[sent{index, rec}] = now
	}
	return true
}

// multicast sends msg, which carries recs, to the group through interface index (0 lets the OS
// choose). force skips answer spacing (announcements).
func (r *Responder) multicast(msg []byte, index int, recs []record, force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.due(index, recs, time.Now(), force) {
		return
	}
	if index > 0 {
		ifi, err := net.InterfaceByIndex(index)
		if err != nil {
			return
		}
		if err := r.pc.SetMulticastInterface(ifi); err != nil {
			r.log.Debug("discovery: choosing the outgoing interface", "interface", ifi.Name, "err", err)
			return
		}
	}
	dst := net.UDPAddrFromAddrPort(netip.AddrPortFrom(group, r.port))
	if _, err := r.pc.WriteTo(msg, nil, dst); err != nil {
		r.log.Debug("discovery: multicasting", "interface", index, "err", err)
	}
}

// announce announces the server twice, one second apart (RFC 6762, section 8.3), on the given
// interfaces; nil means all of them.
func (r *Responder) announce(ctx context.Context, only []link) {
	links := only
	if only == nil {
		r.mu.Lock()
		links = r.links
		r.mu.Unlock()
	}
	for i := range 2 {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		for _, l := range links {
			if z, err := newZone(r.opts.Info(), r.opts.Port, pick(links, l.index, netip.Addr{}, r.opts.Only)); err == nil {
				r.multicast(announcement(z, false), l.index, allRecords, true)
			}
		}
	}
}

// goodbye tells every interface that the server is leaving.
func (r *Responder) goodbye() {
	r.mu.Lock()
	links := r.links
	r.mu.Unlock()
	for _, l := range links {
		if z, err := newZone(r.opts.Info(), r.opts.Port, pick(links, l.index, netip.Addr{}, r.opts.Only)); err == nil {
			r.multicast(announcement(z, true), l.index, allRecords, true)
		}
	}
}

// refresh lists the active interfaces, joins the group on the new ones and returns them.
func (r *Responder) refresh() []link {
	links := activeLinks()
	r.mu.Lock()
	defer r.mu.Unlock()
	var added []link
	for _, l := range links {
		if r.joined[l.index] {
			continue
		}
		ifi, err := net.InterfaceByIndex(l.index)
		if err != nil {
			continue
		}
		// Already a member (the default interface, joined when the port was opened): the error is
		// expected.
		if err := r.pc.JoinGroup(ifi, &net.UDPAddr{IP: net.IP(group.AsSlice())}); err != nil {
			r.log.Debug("discovery: joining the mDNS group", "interface", l.name, "err", err)
		}
		r.joined[l.index] = true
		added = append(added, l)
	}
	r.links = links
	return added
}

// activeLinks lists the interfaces that are up, multicast capable, not loopback, and have an IPv4
// address.
func activeLinks() []link {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []link
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		l := link{index: ifi.Index, name: ifi.Name}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if p, err := netip.ParsePrefix(n.String()); err == nil && p.Addr().Is4() {
					l.prefixes = append(l.prefixes, p)
				}
			}
		}
		if len(l.prefixes) > 0 {
			out = append(out, l)
		}
	}
	return out
}

// linkOf returns the interface with a network that contains addr, or 0.
func linkOf(links []link, addr netip.Addr) int {
	for _, l := range links {
		for _, p := range l.prefixes {
			if p.Contains(addr) {
				return l.index
			}
		}
	}
	return 0
}
