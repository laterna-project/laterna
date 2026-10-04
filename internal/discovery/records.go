// Package discovery announces the server on the local network: DNS-SD over mDNS (RFC 6762 and
// 6763), service "_laterna._tcp". TVs and apps find it without anyone typing its address
// (NsdManager on Android, Bonjour on Apple devices, avahi-browse on Linux). IPv4 only. It knows
// nothing about the database or the catalog.
package discovery

import (
	"errors"
	"net/netip"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// ServiceType is the server's DNS-SD service type.
	ServiceType = "_laterna._tcp.local."
	// enumeration lists the service types present on the network (RFC 6763, section 9).
	enumeration = "_services._dns-sd._udp.local."

	// TTLs (RFC 6762, section 10): 2 min for records that name a host, 75 min for the rest.
	hostTTL  = 120
	otherTTL = 4500
	// legacyTTL caps the TTL in an answer to a plain DNS query (section 6.7).
	legacyTTL = 10

	// cacheFlush is the top bit of the class of a unique record: it replaces what caches hold
	// (section 10.2). In a question the same bit asks for a unicast answer (section 5.4).
	cacheFlush = 1 << 15
	// maxTXT caps each string of the TXT record (the length is one byte).
	maxTXT = 255
)

// Info is what the server says about itself. It is read again for each answer because the name can
// change at runtime.
type Info struct {
	// ID is the server's stable ID (a UUID). It is also used for the instance and host names, which
	// are then unique on the network and survive a rename of the server.
	ID      string
	Name    string
	Version string
}

// zone holds the records we announce.
type zone struct {
	service, enum, instance, host dnsmessage.Name
	port                          uint16
	txt                           []string
	addrs                         []netip.Addr
}

func newZone(info Info, port int, addrs []netip.Addr) (zone, error) {
	label := "laterna-" + strings.ToLower(strings.ReplaceAll(info.ID, "-", ""))
	if len(label) > 63 || strings.ContainsAny(label, ". \\") || port <= 0 || port > 65535 {
		return zone{}, errors.New("discovery: invalid server id or port")
	}
	z := zone{
		service:  dnsmessage.MustNewName(ServiceType),
		enum:     dnsmessage.MustNewName(enumeration),
		instance: dnsmessage.MustNewName(label + "." + ServiceType),
		host:     dnsmessage.MustNewName(label + ".local."),
		port:     uint16(port),
		addrs:    addrs,
	}
	for _, kv := range [][2]string{{"id", info.ID}, {"name", info.Name}, {"version", info.Version}} {
		z.txt = append(z.txt, truncate(kv[0]+"="+kv[1], maxTXT))
	}
	return z, nil
}

// truncate cuts s to n bytes at most without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// record is a kind of record in the zone.
type record int

const (
	recService record = iota // PTR type -> instance
	recEnum                  // PTR enumeration -> type
	recSRV                   // SRV instance -> host and port
	recTXT                   // TXT instance
	recA                     // A host -> addresses
)

// answer answers an mDNS query; nil if it asks for nothing in the zone. legacy means a plain DNS
// query (source port other than 5353): unicast answer with the question and the same ID, capped
// TTLs, no cache-flush bit (RFC 6762, section 6.7). It also returns the records the answer carries,
// because sending them to the group is rate limited (section 6).
func answer(query []byte, z zone, legacy bool) (msg []byte, recs []record) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil || h.Response || h.OpCode != 0 {
		return nil, nil
	}
	var answers, extra []record
	var questions []dnsmessage.Question
	for range 16 {
		q, err := p.Question()
		if err != nil {
			break
		}
		questions = append(questions, q)
		if c := q.Class &^ cacheFlush; c != dnsmessage.ClassINET && c != dnsmessage.ClassANY {
			continue
		}
		all := q.Type == dnsmessage.TypeALL
		switch {
		case sameName(q.Name, z.service) && (all || q.Type == dnsmessage.TypePTR):
			answers = append(answers, recService)
			extra = append(extra, recSRV, recTXT, recA)
		case sameName(q.Name, z.enum) && (all || q.Type == dnsmessage.TypePTR):
			answers = append(answers, recEnum)
		case sameName(q.Name, z.instance):
			if all || q.Type == dnsmessage.TypeSRV {
				answers = append(answers, recSRV)
				extra = append(extra, recA)
			}
			if all || q.Type == dnsmessage.TypeTXT {
				answers = append(answers, recTXT)
			}
		case sameName(q.Name, z.host) && (all || q.Type == dnsmessage.TypeA):
			answers = append(answers, recA)
		}
	}
	if len(answers) == 0 {
		return nil, nil
	}
	answers = unique(answers, nil)
	extra = unique(extra, answers)
	id := uint16(0)
	ttl := func(t uint32) uint32 { return t }
	if legacy {
		id = h.ID
		ttl = func(t uint32) uint32 { return min(t, legacyTTL) }
	} else {
		questions = nil
	}
	if msg = build(z, id, questions, answers, extra, ttl, !legacy); msg == nil {
		return nil, nil
	}
	return msg, append(answers, extra...)
}

// allRecords is the whole zone, as an announcement gives it.
var allRecords = []record{recService, recEnum, recSRV, recTXT, recA}

// announcement is an unsolicited answer with the whole zone (RFC 6762, section 8.3). goodbye sets
// the TTLs to zero so that caches forget it right away (section 10.1).
func announcement(z zone, goodbye bool) []byte {
	ttl := func(t uint32) uint32 { return t }
	if goodbye {
		ttl = func(uint32) uint32 { return 0 }
	}
	return build(z, 0, nil, allRecords, nil, ttl, true)
}

func build(z zone, id uint16, questions []dnsmessage.Question, answers, extra []record, ttl func(uint32) uint32, flush bool) []byte {
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{ID: id, Response: true, Authoritative: true})
	b.EnableCompression()
	err := b.StartQuestions()
	for _, q := range questions {
		err = errors.Join(err, b.Question(q))
	}
	err = errors.Join(err, b.StartAnswers())
	for _, r := range answers {
		err = errors.Join(err, z.write(&b, r, ttl, flush))
	}
	err = errors.Join(err, b.StartAdditionals())
	for _, r := range extra {
		err = errors.Join(err, z.write(&b, r, ttl, flush))
	}
	msg, ferr := b.Finish()
	if errors.Join(err, ferr) != nil {
		return nil
	}
	return msg
}

// write adds a record. flush sets the cache-flush bit on unique records (all but the PTRs, which
// are shared between servers).
func (z zone) write(b *dnsmessage.Builder, r record, ttl func(uint32) uint32, flush bool) error {
	class := dnsmessage.ClassINET
	if flush && r != recService && r != recEnum {
		class |= cacheFlush
	}
	head := func(name dnsmessage.Name, t uint32) dnsmessage.ResourceHeader {
		return dnsmessage.ResourceHeader{Name: name, Class: class, TTL: ttl(t)}
	}
	switch r {
	case recService:
		return b.PTRResource(head(z.service, otherTTL), dnsmessage.PTRResource{PTR: z.instance})
	case recEnum:
		return b.PTRResource(head(z.enum, otherTTL), dnsmessage.PTRResource{PTR: z.service})
	case recSRV:
		return b.SRVResource(head(z.instance, hostTTL), dnsmessage.SRVResource{Target: z.host, Port: z.port})
	case recTXT:
		return b.TXTResource(head(z.instance, otherTTL), dnsmessage.TXTResource{TXT: z.txt})
	case recA:
		var err error
		for _, a := range z.addrs {
			if a.Is4() {
				err = errors.Join(err, b.AResource(head(z.host, hostTTL), dnsmessage.AResource{A: a.As4()}))
			}
		}
		return err
	default:
		return nil
	}
}

// unique removes duplicates from list, and whatever is already in except.
func unique(list, except []record) []record {
	seen := map[record]bool{}
	for _, r := range except {
		seen[r] = true
	}
	out := list[:0:0]
	for _, r := range list {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// sameName compares two names ignoring case (RFC 6762, section 16).
func sameName(a, b dnsmessage.Name) bool { return strings.EqualFold(a.String(), b.String()) }

// link is an active network interface and its IPv4 addresses.
type link struct {
	index    int
	name     string
	prefixes []netip.Prefix
}

// local reports whether a query comes from the local network (RFC 6762, section 5.5): from the
// machine itself, or from an address on a network one of the active interfaces is attached to.
// Everything else is ignored: a server reachable from the Internet tells strangers nothing about
// itself, and does not act as a reflector towards a spoofed address (the answer is several times
// the size of the query).
func local(links []link, src netip.Addr) bool {
	return src.IsLoopback() || linkOf(links, src) != 0
}

// pick chooses the addresses to give a client: those of the interface its query came in on (never a
// Docker bridge address for a client in the living room), otherwise those of the interface whose
// network contains its address, otherwise all of them. only, if valid, wins: the server listens on
// nothing else.
func pick(links []link, index int, src netip.Addr, only netip.Addr) []netip.Addr {
	if only.IsValid() {
		return []netip.Addr{only}
	}
	addrsOf := func(l link) []netip.Addr {
		out := make([]netip.Addr, 0, len(l.prefixes))
		for _, p := range l.prefixes {
			out = append(out, p.Addr())
		}
		return out
	}
	if index > 0 {
		for _, l := range links {
			if l.index == index {
				return addrsOf(l)
			}
		}
	}
	for _, l := range links {
		for _, p := range l.prefixes {
			if p.Contains(src) {
				return addrsOf(l)
			}
		}
	}
	var all []netip.Addr
	for _, l := range links {
		all = append(all, addrsOf(l)...)
	}
	return all
}
