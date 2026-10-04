package discovery

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

var testInfo = Info{ID: "0192f0c4-7d1e-7a3b-9c2d-1e2f3a4b5c6d", Name: "Salon", Version: "0.3.0"}

func testZone(t *testing.T) zone {
	t.Helper()
	z, err := newZone(testInfo, 8096, []netip.Addr{netip.MustParseAddr("192.168.1.20")})
	if err != nil {
		t.Fatal(err)
	}
	return z
}

// query builds an mDNS query (or a plain DNS one, with an ID).
func query(t *testing.T, id uint16, questions ...dnsmessage.Question) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	for _, q := range questions {
		if err := b.Question(q); err != nil {
			t.Fatal(err)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func question(name string, typ dnsmessage.Type) dnsmessage.Question {
	return dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET}
}

type parsed struct {
	header     dnsmessage.Header
	questions  int
	answers    []dnsmessage.Resource
	additional []dnsmessage.Resource
}

func parse(t *testing.T, msg []byte) parsed {
	t.Helper()
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		t.Fatal(err)
	}
	qs, err := p.AllQuestions()
	if err != nil {
		t.Fatal(err)
	}
	ans, err := p.AllAnswers()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SkipAllAuthorities(); err != nil {
		t.Fatal(err)
	}
	add, err := p.AllAdditionals()
	if err != nil {
		t.Fatal(err)
	}
	return parsed{header: h, questions: len(qs), answers: ans, additional: add}
}

func types(rs []dnsmessage.Resource) []dnsmessage.Type {
	out := make([]dnsmessage.Type, len(rs))
	for i, r := range rs {
		out[i] = r.Header.Type
	}
	return out
}

// Browsing for the service returns the instance, plus what is needed to reach it without another
// query.
func TestAnswerBrowse(t *testing.T) {
	z := testZone(t)
	resp, recs := answer(query(t, 0, question("_laterna._tcp.local.", dnsmessage.TypePTR)), z, false)
	if resp == nil {
		t.Fatal("no answer")
	}
	if !slices.Equal(recs, []record{recService, recSRV, recTXT, recA}) {
		t.Errorf("records of the answer: %v", recs)
	}
	got := parse(t, resp)
	if !got.header.Response || !got.header.Authoritative || got.header.ID != 0 || got.questions != 0 {
		t.Errorf("header: %+v, %d questions", got.header, got.questions)
	}
	if !slices.Equal(types(got.answers), []dnsmessage.Type{dnsmessage.TypePTR}) {
		t.Fatalf("answers: %v", types(got.answers))
	}
	ptr := got.answers[0].Body.(*dnsmessage.PTRResource)
	instance := "laterna-0192f0c47d1e7a3b9c2d1e2f3a4b5c6d._laterna._tcp.local."
	if ptr.PTR.String() != instance {
		t.Errorf("instance: %s", ptr.PTR)
	}
	if got.answers[0].Header.Class != dnsmessage.ClassINET || got.answers[0].Header.TTL != otherTTL {
		t.Errorf("shared PTR, no cache-flush bit: %+v", got.answers[0].Header)
	}
	if !slices.Equal(types(got.additional), []dnsmessage.Type{dnsmessage.TypeSRV, dnsmessage.TypeTXT, dnsmessage.TypeA}) {
		t.Fatalf("additional records: %v", types(got.additional))
	}
	srv := got.additional[0].Body.(*dnsmessage.SRVResource)
	if srv.Port != 8096 || srv.Target.String() != "laterna-0192f0c47d1e7a3b9c2d1e2f3a4b5c6d.local." {
		t.Errorf("SRV: %+v", srv)
	}
	if got.additional[0].Header.Class != dnsmessage.ClassINET|cacheFlush || got.additional[0].Header.TTL != hostTTL {
		t.Errorf("unique SRV, flushes caches: %+v", got.additional[0].Header)
	}
	txt := got.additional[1].Body.(*dnsmessage.TXTResource)
	if !slices.Equal(txt.TXT, []string{"id=" + testInfo.ID, "name=Salon", "version=0.3.0"}) {
		t.Errorf("TXT: %q", txt.TXT)
	}
	if a := got.additional[2].Body.(*dnsmessage.AResource); a.A != [4]byte{192, 168, 1, 20} {
		t.Errorf("A: %v", a.A)
	}
}

func TestAnswerQuestions(t *testing.T) {
	z := testZone(t)
	instance := "laterna-0192f0c47d1e7a3b9c2d1e2f3a4b5c6d._laterna._tcp.local."
	host := "LATERNA-0192F0C47D1E7A3B9C2D1E2F3A4B5C6D.local." // case does not matter
	for _, tc := range []struct {
		name          string
		questions     []dnsmessage.Question
		answers, more []dnsmessage.Type
	}{
		{
			"service enumeration",
			[]dnsmessage.Question{question("_services._dns-sd._udp.local.", dnsmessage.TypePTR)},
			[]dnsmessage.Type{dnsmessage.TypePTR},
			nil,
		},
		{
			"SRV de l'instance",
			[]dnsmessage.Question{question(instance, dnsmessage.TypeSRV)},
			[]dnsmessage.Type{dnsmessage.TypeSRV},
			[]dnsmessage.Type{dnsmessage.TypeA},
		},
		{
			"tout de l'instance",
			[]dnsmessage.Question{question(instance, dnsmessage.TypeALL)},
			[]dnsmessage.Type{dnsmessage.TypeSRV, dnsmessage.TypeTXT},
			[]dnsmessage.Type{dnsmessage.TypeA},
		},
		{
			"host address",
			[]dnsmessage.Question{question(host, dnsmessage.TypeA)},
			[]dnsmessage.Type{dnsmessage.TypeA},
			nil,
		},
		{"SRV and A asked together: A is not repeated", []dnsmessage.Question{
			question(instance, dnsmessage.TypeSRV), question(host, dnsmessage.TypeA),
		}, []dnsmessage.Type{dnsmessage.TypeSRV, dnsmessage.TypeA}, nil},
		{"unicast answer requested (QU bit)", []dnsmessage.Question{{
			Name: dnsmessage.MustNewName("_laterna._tcp.local."), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET | cacheFlush,
		}}, []dnsmessage.Type{dnsmessage.TypePTR}, []dnsmessage.Type{dnsmessage.TypeSRV, dnsmessage.TypeTXT, dnsmessage.TypeA}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := answer(query(t, 0, tc.questions...), z, false)
			if resp == nil {
				t.Fatal("no answer")
			}
			got := parse(t, resp)
			if !slices.Equal(types(got.answers), tc.answers) || !slices.Equal(types(got.additional), tc.more) {
				t.Errorf("answers %v, additionals %v", types(got.answers), types(got.additional))
			}
		})
	}
}

func TestAnswerIgnores(t *testing.T) {
	z := testZone(t)
	for name, msg := range map[string][]byte{
		"autre service":    query(t, 0, question("_googlecast._tcp.local.", dnsmessage.TypePTR)),
		"autre type":       query(t, 0, question("_laterna._tcp.local.", dnsmessage.TypeTXT)),
		"IPv6 of the host": query(t, 0, question("laterna-0192f0c47d1e7a3b9c2d1e2f3a4b5c6d.local.", dnsmessage.TypeAAAA)),
		"truncated":        query(t, 0, question("_laterna._tcp.local.", dnsmessage.TypePTR))[:14],
		"vide":             nil,
	} {
		if resp, recs := answer(msg, z, false); resp != nil || recs != nil {
			t.Errorf("%s: unexpected answer", name)
		}
	}
	// An answer from another device calls for no answer.
	resp, _ := answer(query(t, 0, question("_laterna._tcp.local.", dnsmessage.TypePTR)), z, false)
	if again, _ := answer(resp, z, false); again != nil {
		t.Error("answered an answer")
	}
}

// A plain DNS query (dig -p 5353 @224.0.0.251) gets a unicast answer: same ID, question echoed,
// short TTLs, no cache-flush bit.
func TestAnswerLegacy(t *testing.T) {
	z := testZone(t)
	direct, _ := answer(query(t, 4242, question("_laterna._tcp.local.", dnsmessage.TypePTR)), z, true)
	got := parse(t, direct)
	if got.header.ID != 4242 || got.questions != 1 {
		t.Errorf("header: %+v, %d questions", got.header, got.questions)
	}
	for _, r := range append(got.answers, got.additional...) {
		if r.Header.TTL > legacyTTL || r.Header.Class != dnsmessage.ClassINET {
			t.Errorf("%v: TTL %d, class %v", r.Header.Type, r.Header.TTL, r.Header.Class)
		}
	}
}

func TestAnnouncement(t *testing.T) {
	z := testZone(t)
	got := parse(t, announcement(z, false))
	want := []dnsmessage.Type{dnsmessage.TypePTR, dnsmessage.TypePTR, dnsmessage.TypeSRV, dnsmessage.TypeTXT, dnsmessage.TypeA}
	if !slices.Equal(types(got.answers), want) || len(got.additional) != 0 {
		t.Fatalf("announcement: %v + %v", types(got.answers), types(got.additional))
	}
	for _, r := range parse(t, announcement(z, true)).answers {
		if r.Header.TTL != 0 {
			t.Errorf("goodbye: %v keeps a TTL of %d", r.Header.Type, r.Header.TTL)
		}
	}
}

func TestZone(t *testing.T) {
	long := Info{ID: testInfo.ID, Name: strings.Repeat("é", 200), Version: "dev"}
	z, err := newZone(long, 8096, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := z.txt[1]
	if len(name) > maxTXT || !strings.HasPrefix(name, "name=é") || !strings.HasSuffix(name, "é") {
		t.Errorf("name cut on a character boundary, %d bytes: %q", len(name), name[len(name)-4:])
	}
	for _, bad := range []struct {
		info Info
		port int
	}{{Info{ID: "a.b"}, 8096}, {testInfo, 0}, {testInfo, 70000}, {Info{ID: strings.Repeat("a", 60)}, 8096}} {
		if _, err := newZone(bad.info, bad.port, nil); err == nil {
			t.Errorf("accepted: %+v", bad)
		}
	}
}

func TestPick(t *testing.T) {
	lan := link{index: 2, name: "eth0", prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.20/24")}}
	docker := link{index: 3, name: "docker0", prefixes: []netip.Prefix{netip.MustParsePrefix("172.17.0.1/16")}}
	links := []link{docker, lan}
	addr := netip.MustParseAddr
	for _, tc := range []struct {
		name  string
		index int
		src   netip.Addr
		only  netip.Addr
		want  []netip.Addr
	}{
		{"incoming interface known", 2, addr("10.0.0.5"), netip.Addr{}, []netip.Addr{addr("192.168.1.20")}},
		{"client's network", 0, addr("192.168.1.42"), netip.Addr{}, []netip.Addr{addr("192.168.1.20")}},
		{"conteneur", 0, addr("172.17.0.2"), netip.Addr{}, []netip.Addr{addr("172.17.0.1")}},
		{"unknown: all of them", 0, addr("10.9.9.9"), netip.Addr{}, []netip.Addr{addr("172.17.0.1"), addr("192.168.1.20")}},
		{"forced listen address", 3, addr("172.17.0.2"), addr("192.168.1.20"), []netip.Addr{addr("192.168.1.20")}},
	} {
		if got := pick(links, tc.index, tc.src, tc.only); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	if linkOf(links, addr("192.168.1.99")) != 2 || linkOf(links, addr("8.8.8.8")) != 0 {
		t.Error("linkOf")
	}
}

// Only the local network is served (RFC 6762, section 5.5): the machine itself and the networks of
// its active interfaces, never an address from elsewhere.
func TestLocal(t *testing.T) {
	links := []link{
		{index: 2, name: "eth0", prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.1.20/24")}},
		{index: 3, name: "docker0", prefixes: []netip.Prefix{netip.MustParsePrefix("172.17.0.1/16")}},
	}
	for src, want := range map[string]bool{
		"192.168.1.42": true,  // the TV in the living room
		"192.168.1.20": true,  // the machine itself, by its LAN address
		"172.17.0.2":   true,  // a container
		"127.0.0.1":    true,  // the machine itself
		"192.168.2.42": false, // another private network, behind a router
		"10.8.0.2":     false, // a VPN without multicast
		"203.0.113.9":  false, // Internet
		"8.8.8.8":      false,
	} {
		if got := local(links, netip.MustParseAddr(src)); got != want {
			t.Errorf("local(%s) = %v", src, got)
		}
	}
	if local(nil, netip.MustParseAddr("192.168.1.42")) || !local(nil, netip.MustParseAddr("127.0.0.1")) {
		t.Error("no active interface: only the machine itself")
	}
}

func FuzzAnswer(f *testing.F) {
	z, _ := newZone(testInfo, 8096, []netip.Addr{netip.MustParseAddr("192.168.1.20")})
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	_ = b.StartQuestions()
	_ = b.Question(question("_laterna._tcp.local.", dnsmessage.TypePTR))
	seed, _ := b.Finish()
	f.Add(seed, false)
	f.Fuzz(func(t *testing.T, msg []byte, legacy bool) {
		if resp, _ := answer(msg, z, legacy); resp != nil {
			var p dnsmessage.Parser
			if _, err := p.Start(resp); err != nil {
				t.Fatalf("unreadable answer: %v", err)
			}
		}
	})
}
