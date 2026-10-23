package wifilan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

func selectedLANPeer(iface net.Interface, prefixes []netip.Prefix, endpoint string) (netip.AddrPort, error) {
	address, err := netip.ParseAddrPort(endpoint)
	if err != nil || address.Port() == 0 || !onLinkIPv4(address.Addr(), prefixes) {
		return netip.AddrPort{}, errors.New("Wi-Fi requires a numeric peer on the selected directly connected IPv4 LAN; hostnames and routed peers are refused")
	}
	for _, prefix := range prefixes {
		if address.Addr() == prefix.Addr() {
			return netip.AddrPort{}, errors.New("Wi-Fi peer address belongs to this host")
		}
	}
	if iface.Index <= 0 || iface.Name == "" {
		return netip.AddrPort{}, errors.New("Wi-Fi requires an explicit LAN interface")
	}
	return address, nil
}

// DialSelectedLAN never resolves names or falls back to the default route.
// Socket-level interface and no-gateway restrictions remain in effect for the
// entire connection, including after a VPN/default-route change.
func DialSelectedLAN(ctx context.Context, interfaceName, endpoint string) (_ net.Conn, result error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	iface, prefixes, err := SelectInterface(interfaceName)
	if err != nil {
		return nil, err
	}
	address, err := selectedLANPeer(iface, prefixes, endpoint)
	if err != nil {
		return nil, err
	}
	var source netip.Addr
	for _, prefix := range prefixes {
		if prefix.Contains(address.Addr()) {
			source = prefix.Addr()
			break
		}
	}
	dialer := net.Dialer{
		Timeout:   5 * time.Second,
		LocalAddr: &net.TCPAddr{IP: net.IP(source.AsSlice())},
		Control: func(_, _ string, raw syscall.RawConn) error {
			var restrictionError error
			if err := raw.Control(func(fd uintptr) {
				restrictionError = restrictLANSocket(fd, iface)
			}); err != nil {
				return err
			}
			return restrictionError
		},
	}
	connection, err := dialer.DialContext(ctx, "tcp4", address.String())
	if err != nil {
		return nil, fmt.Errorf("connect tablet on selected LAN without a gateway: %w", err)
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, connection.Close())
		}
	}()
	current, currentPrefixes, err := SelectInterface(iface.Name)
	if err != nil {
		return nil, err
	}
	if err := validateInterfaceSnapshot(iface, prefixes, current, currentPrefixes); err != nil {
		return nil, err
	}
	return connection, nil
}
