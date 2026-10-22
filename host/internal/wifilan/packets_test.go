package wifilan

import (
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const testInstance = "Tablet._drawsplay-tablet._tcp.local."

func serviceRecords() []dns.RR {
	return []dns.RR{
		&dns.PTR{Hdr: dns.RR_Header{Name: TabletService + ".local.", Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 120}, Ptr: testInstance},
		&dns.SRV{Hdr: dns.RR_Header{Name: testInstance, Rrtype: dns.TypeSRV, Class: dns.ClassINET, Ttl: 120}, Port: 37183, Target: "tablet.local."},
		&dns.TXT{Hdr: dns.RR_Header{Name: testInstance, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 120}, Txt: []string{"v=2", "videoPort=37184"}},
		&dns.A{Hdr: dns.RR_Header{Name: "tablet.local.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 120}, A: net.ParseIP("192.0.2.20")},
	}
}

func packedResponse(t *testing.T, records []dns.RR) []byte {
	t.Helper()
	msg := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true, Authoritative: true}, Answer: records}
	data, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPacketCacheAssemblesSplitTabletRecords(t *testing.T) {
	now := time.Now()
	cache := newPacketCache()
	records := serviceRecords()
	prefixes := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")}
	first, err := cache.consume(packedResponse(t, records[:3]), now, prefixes)
	if err != nil || len(first) != 0 {
		t.Fatalf("incomplete record accepted: %v %v", first, err)
	}
	second, err := cache.consume(packedResponse(t, records[3:]), now, prefixes)
	if err != nil || len(second) != 1 || second[0].Addresses[0].String() != "192.0.2.20" {
		t.Fatalf("split records not assembled: %v %v", second, err)
	}
}

func TestPacketCacheExpiresRecordsAndHonorsGoodbye(t *testing.T) {
	now := time.Now()
	cache := newPacketCache()
	prefixes := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")}
	if _, err := cache.consume(packedResponse(t, serviceRecords()), now, prefixes); err != nil {
		t.Fatal(err)
	}
	goodbye := serviceRecords()[0]
	goodbye.Header().Ttl = 0
	got, err := cache.consume(packedResponse(t, []dns.RR{goodbye}), now, prefixes)
	if err != nil || len(got) != 0 {
		t.Fatalf("goodbye retained tablet: %v %v", got, err)
	}
	got, err = cache.consume(packedResponse(t, serviceRecords()[3:]), now.Add(121*time.Second), prefixes)
	if err != nil || len(got) != 0 {
		t.Fatalf("expired tablet retained: %v %v", got, err)
	}
}

func TestPacketCacheRejectsOversizedAndMalformedPackets(t *testing.T) {
	cache := newPacketCache()
	for _, data := range [][]byte{nil, {0, 1, 2}, make([]byte, 4097)} {
		if _, err := cache.consume(data, time.Now(), nil); err == nil {
			t.Fatal("invalid DNS packet accepted")
		}
	}
}

func TestPacketCacheHasHardCapacity(t *testing.T) {
	cache := newPacketCache()
	for i := range 65 {
		record := &dns.A{
			Hdr: dns.RR_Header{Name: fmt.Sprintf("peer-%d.local.", i), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 120},
			A:   net.ParseIP("192.0.2.20"),
		}
		_, err := cache.consume(packedResponse(t, []dns.RR{record}), time.Now(), nil)
		if i < 64 && err != nil {
			t.Fatal(err)
		}
		if i == 64 && err == nil {
			t.Fatal("unbounded discovery cache accepted 65th hostname")
		}
	}
}

func FuzzDiscoveryPackets(f *testing.F) {
	message := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}, Answer: serviceRecords()}
	data, err := message.Pack()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(data)
	f.Add([]byte{0, 1, 2})
	f.Fuzz(func(t *testing.T, data []byte) {
		cache := newPacketCache()
		_, _ = cache.consume(data, time.Now(), []netip.Prefix{
			netip.MustParsePrefix("192.0.2.10/24"),
		})
		if len(cache.hosts) > 64 || len(cache.services) > 64 {
			t.Fatal("packet exceeded bounded cache capacity")
		}
		for _, host := range cache.hosts {
			if len(host.addresses) > 8 {
				t.Fatal("packet exceeded per-host address bound")
			}
		}
	})
}

func TestRejectedCompleteAdvertisementDoesNotPoisonLaterPackets(t *testing.T) {
	cache := newPacketCache()
	records := serviceRecords()
	records[2].(*dns.TXT).Txt[0] = "v=1"
	prefixes := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")}
	if _, err := cache.consume(packedResponse(t, records), time.Now(), prefixes); err == nil {
		t.Fatal("unsupported version accepted")
	}
	if _, err := cache.consume(packedResponse(t, serviceRecords()[3:]), time.Now(), prefixes); err != nil {
		t.Fatalf("invalid prior service poisoned later packet processing: %v", err)
	}
}
