package wifilan

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/grandcat/zeroconf"
)

func tabletEntry() *zeroconf.ServiceEntry {
	entry := zeroconf.NewServiceEntry("Test tablet", "_drawsplay-tablet._tcp", "local.")
	entry.Port = 37183
	entry.TTL = 120
	entry.Text = []string{"v=2", "videoPort=37184"}
	entry.AddrIPv4 = []net.IP{net.ParseIP("192.0.2.20")}
	return entry
}

func TestTabletCandidateUsesOnlySelectedOnLinkAddresses(t *testing.T) {
	entry := tabletEntry()
	entry.AddrIPv4 = append(entry.AddrIPv4,
		net.ParseIP("198.51.100.2"), net.ParseIP("127.0.0.1"),
		net.ParseIP("192.0.2.0"), net.ParseIP("192.0.2.255"),
		net.ParseIP("224.0.0.1"), net.ParseIP("192.0.2.20"))
	got, err := ParseCandidate(entry, []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Addresses) != 1 || got.Addresses[0] != netip.MustParseAddr("192.0.2.20") {
		t.Fatalf("unsafe or duplicate addresses retained: %v", got.Addresses)
	}
	if got.InputPort != 37183 || got.VideoPort != 37184 || got.Name != "Test tablet" {
		t.Fatalf("incorrect endpoint: %+v", got)
	}
}

func TestTabletCandidateRejectsUnusableOrAmbiguousAdvertisements(t *testing.T) {
	tests := map[string]func(*zeroconf.ServiceEntry){
		"old host service": func(e *zeroconf.ServiceEntry) { e.Service = "_drawsplay._tcp" },
		"wrong domain":     func(e *zeroconf.ServiceEntry) { e.Domain = "example.org" },
		"old protocol":     func(e *zeroconf.ServiceEntry) { e.Text[0] = "v=1" },
		"missing version":  func(e *zeroconf.ServiceEntry) { e.Text = []string{"videoPort=37184"} },
		"duplicate key":    func(e *zeroconf.ServiceEntry) { e.Text = append(e.Text, "v=2") },
		"bad video port":   func(e *zeroconf.ServiceEntry) { e.Text[1] = "videoPort=65536" },
		"same ports":       func(e *zeroconf.ServiceEntry) { e.Text[1] = "videoPort=37183" },
		"privileged port":  func(e *zeroconf.ServiceEntry) { e.Port = 22 },
		"expired":          func(e *zeroconf.ServiceEntry) { e.TTL = 0 },
		"unsafe name":      func(e *zeroconf.ServiceEntry) { e.Instance = "tablet\nforged log" },
		"no address":       func(e *zeroconf.ServiceEntry) { e.AddrIPv4 = nil },
		"off link":         func(e *zeroconf.ServiceEntry) { e.AddrIPv4 = []net.IP{net.ParseIP("198.51.100.2")} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			entry := tabletEntry()
			mutate(entry)
			if _, err := ParseCandidate(entry, []netip.Prefix{
				netip.MustParsePrefix("192.0.2.10/24"),
			}); err == nil {
				t.Fatal("invalid advertisement accepted")
			}
		})
	}
}

func TestTabletCandidateNeverResolvesAdvertisedHostnames(t *testing.T) {
	entry := tabletEntry()
	entry.HostName = "untrusted.example.org."
	entry.Text = append(entry.Text, "tlsFp=attacker-controlled")
	got, err := ParseCandidate(entry, []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Addresses) != 1 || got.Addresses[0].String() != "192.0.2.20" {
		t.Fatalf("advertised hostname affected numeric endpoint: %+v", got)
	}
}

func TestTabletCandidateRejectsDefaultRouteAsLANPrefix(t *testing.T) {
	if _, err := ParseCandidate(tabletEntry(), []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/0"),
	}); err == nil {
		t.Fatal("default route treated as local network")
	}
}

func TestTabletCandidateBoundsUntrustedData(t *testing.T) {
	entry := tabletEntry()
	for range 64 {
		entry.Text = append(entry.Text, "unknown=extra")
	}
	if _, err := ParseCandidate(entry, []netip.Prefix{
		netip.MustParsePrefix("192.0.2.10/24"),
	}); err == nil {
		t.Fatal("unbounded TXT metadata accepted")
	}
}

func TestDiscoveryRequiresExplicitUsableInterface(t *testing.T) {
	for _, name := range []string{"", "drawsplay-no-such-interface"} {
		if _, _, err := SelectInterface(name); err == nil {
			t.Fatalf("invalid interface %q accepted", name)
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			if _, _, err := SelectInterface(iface.Name); err == nil {
				t.Fatal("loopback accepted as a tablet LAN")
			}
		}
	}
}

func TestCancelledDiscoveryDoesNotStartNetworkWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Browse(ctx, "", func(Candidate) {}, func(error) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation before interface lookup, got %v", err)
	}
}

func TestTabletCandidateRejectsEveryLocalAddressWithOverlappingPrefixes(t *testing.T) {
	entry := tabletEntry()
	entry.AddrIPv4 = []net.IP{net.ParseIP("192.0.2.10")}
	if _, err := ParseCandidate(entry, []netip.Prefix{
		netip.MustParsePrefix("192.0.2.10/24"),
		netip.MustParsePrefix("192.0.2.11/24"),
	}); err == nil {
		t.Fatal("another address on this host mistaken for a tablet")
	}
}

func FuzzTabletCandidate(f *testing.F) {
	f.Add("v=2", "videoPort=37184", []byte{192, 0, 2, 20}, 37183)
	f.Add("v=1", "videoPort=0", []byte{127, 0, 0, 1}, 0)
	f.Fuzz(func(t *testing.T, version, video string, address []byte, port int) {
		entry := tabletEntry()
		entry.Text = []string{version, video}
		entry.AddrIPv4 = []net.IP{net.IP(address)}
		entry.Port = port
		got, err := ParseCandidate(entry, []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")})
		if err != nil {
			return
		}
		if len(got.Addresses) != 1 || !netip.MustParsePrefix("192.0.2.0/24").Contains(got.Addresses[0]) ||
			got.InputPort < 1024 || got.InputPort > 65535 ||
			got.VideoPort < 1024 || got.VideoPort > 65535 || got.InputPort == got.VideoPort {
			t.Fatalf("unsafe successful candidate: %+v", got)
		}
	})
}
