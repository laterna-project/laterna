package discovery

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The server answers a plain DNS query sent to its port with a unicast answer. This covers the
// whole network path without relying on multicast on the test machine.
func TestResponderUnicastQuery(t *testing.T) {
	r, err := Listen(Options{Port: 8096, Info: func() Info { return testInfo }, listen: "224.0.0.251:0"})
	if err != nil {
		t.Skipf("multicast not available on this machine: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- r.Serve(ctx) }()

	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.Port()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(query(t, 7, question("_laterna._tcp.local.", dnsmessage.TypePTR))); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, buf[:n])
	if got.header.ID != 7 || !slices.Contains(types(got.additional), dnsmessage.TypeSRV) {
		t.Fatalf("answer: %+v, %v", got.header, types(got.additional))
	}
	srv := got.additional[0].Body.(*dnsmessage.SRVResource)
	if srv.Port != 8096 {
		t.Errorf("announced port: %d", srv.Port)
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve does not stop")
	}
}

// A query from outside the local network gets nothing: no unicast answer (the server would reflect
// traffic to a spoofed address) and no answer to the group.
func TestResponderIgnoresOffLinkQueries(t *testing.T) {
	r, err := Listen(Options{Port: 8096, Info: func() Info { return testInfo }, listen: "224.0.0.251:0"})
	if err != nil {
		t.Skipf("multicast not available on this machine: %v", err)
	}
	defer func() { _ = r.conn.Close() }()
	browse := query(t, 7, question("_laterna._tcp.local.", dnsmessage.TypePTR))
	for _, src := range []string{"203.0.113.9:40000", "203.0.113.9:5353", "198.51.100.77:53"} {
		if resp, _, _, _ := r.reply(browse, netip.MustParseAddrPort(src), 0); resp != nil {
			t.Errorf("query from %s served", src)
		}
	}
	// A stranger must not make us list the interfaces on every packet.
	r.mu.Lock()
	checked := r.checked
	r.mu.Unlock()
	if checked.IsZero() {
		t.Error("interfaces were not checked for an unknown address")
	}
	if resp, _, _, _ := r.reply(browse, netip.MustParseAddrPort("203.0.113.9:40001"), 0); resp != nil {
		t.Error("second query served")
	}
	r.mu.Lock()
	again := r.checked
	r.mu.Unlock()
	if !again.Equal(checked) {
		t.Error("interfaces checked on every packet from a stranger")
	}
	// The machine itself is still served, with a unicast answer like any plain DNS query.
	resp, _, _, legacy := r.reply(browse, netip.MustParseAddrPort("127.0.0.1:40000"), 0)
	if resp == nil || !legacy {
		t.Fatalf("local query: %v, unicast %v", resp != nil, legacy)
	}
	if got := parse(t, resp); got.header.ID != 7 || !slices.Contains(types(got.additional), dnsmessage.TypeSRV) {
		t.Errorf("answer: %+v", got.header)
	}
}

// Spacing of answers to the group (RFC 6762, section 6): the same record does not go out again
// within a second on an interface, but a different answer does not wait for the previous one.
func TestSpacing(t *testing.T) {
	r := &Responder{last: map[sent]time.Time{}}
	browse := []record{recService, recSRV, recTXT, recA}
	now := time.Now()
	if !r.due(2, browse, now, false) {
		t.Fatal("first answer held back")
	}
	if r.due(2, browse, now.Add(300*time.Millisecond), false) {
		t.Error("same answer sent again within a second")
	}
	// Service enumeration has nothing in common with browsing: it goes out.
	if !r.due(2, []record{recEnum}, now.Add(400*time.Millisecond), false) {
		t.Error("different answer held back by the previous one")
	}
	// Everything it would carry just went out: held back.
	if r.due(2, []record{recSRV, recA}, now.Add(500*time.Millisecond), false) {
		t.Error("records already sent were sent again")
	}
	// Another interface has its own timer, and an announcement skips the check.
	if !r.due(3, browse, now.Add(500*time.Millisecond), false) {
		t.Error("other interface held back")
	}
	if !r.due(2, allRecords, now.Add(600*time.Millisecond), true) {
		t.Error("announcement held back")
	}
	// The announcement just sent everything: browsing waits, then goes out once the second has
	// passed.
	if r.due(2, browse, now.Add(900*time.Millisecond), false) {
		t.Error("answer sent again right after an announcement")
	}
	if !r.due(2, browse, now.Add(1700*time.Millisecond), false) {
		t.Error("answer held back for more than a second")
	}
}
