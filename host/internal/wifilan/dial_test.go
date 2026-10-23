package wifilan

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"testing"
)

func TestLANDialRejectsOffLinkAndNonNumericEndpointsBeforeConnecting(t *testing.T) {
	iface := net.Interface{Index: 1, Name: "test-lan", Flags: net.FlagUp | net.FlagMulticast}
	prefixes := []netip.Prefix{netip.MustParsePrefix("192.168.20.4/24")}
	for _, endpoint := range []string{
		"example.com:37183", "8.8.8.8:37183", "192.168.21.4:37183",
		"127.0.0.1:37183", "[::1]:37183", "192.168.20.255:37183",
		"192.168.20.0:37183", "192.168.20.4:37183", "192.168.20.5:0",
	} {
		if _, err := selectedLANPeer(iface, prefixes, endpoint); err == nil {
			t.Fatalf("unsafe or invalid LAN endpoint admitted: %s", endpoint)
		}
	}
	address, err := selectedLANPeer(iface, prefixes, "192.168.20.5:37183")
	if err != nil || address != netip.MustParseAddrPort("192.168.20.5:37183") {
		t.Fatalf("valid directly connected peer rejected: %v", err)
	}
}

func TestLANDialRejectsInvalidSelectionAndCancelledWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if connection, err := DialSelectedLAN(ctx, "", "192.168.20.5:37183"); err == nil || connection != nil {
		t.Fatal("cancelled/unselected dial created a connection")
	}
	if connection, err := DialSelectedLAN(context.Background(), "", "192.168.20.5:37183"); err == nil || connection != nil {
		t.Fatal("implicit-interface dial created a connection")
	}
}

func TestPairedLANDialRejectsMissingTrustBeforeOpeningAnyNetwork(t *testing.T) {
	if connection, err := DialPairedLAN(context.Background(), "does-not-exist", "8.8.8.8:37183", tls.Certificate{}, [32]byte{}); err == nil || connection != nil {
		t.Fatal("unpaired or invalid local identity attempted a paired LAN session")
	}
}
