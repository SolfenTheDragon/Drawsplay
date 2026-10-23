package wifilan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

func SelectInterface(name string) (net.Interface, []netip.Prefix, error) {
	if name == "" {
		return net.Interface{}, nil, fmt.Errorf("select a LAN interface explicitly; refusing all-interface discovery")
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return net.Interface{}, nil, fmt.Errorf("find selected LAN interface: %w", err)
	}
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 ||
		iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
		return net.Interface{}, nil, fmt.Errorf("interface %q must be up, multicast-capable and not loopback/point-to-point", name)
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return net.Interface{}, nil, fmt.Errorf("read LAN interface addresses: %w", err)
	}
	var prefixes []netip.Prefix
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			return net.Interface{}, nil, fmt.Errorf("parse LAN interface address: %w", err)
		}
		if prefix.Addr().Is4() && prefix.Addr().IsGlobalUnicast() && prefix.Bits() >= 1 {
			prefixes = append(prefixes, prefix)
		}
	}
	if len(prefixes) == 0 {
		return net.Interface{}, nil, fmt.Errorf("interface %q has no usable IPv4 LAN prefix; IPv6 discovery is not implemented", name)
	}
	return *iface, prefixes, nil
}

// Browse only reports untrusted candidates. It never dials, pairs or captures.
// Callbacks run serially and must return promptly.
func Browse(ctx context.Context, interfaceName string, found func(Candidate), rejected func(error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if found == nil || rejected == nil {
		return fmt.Errorf("discovery requires candidate and rejection handlers")
	}
	iface, prefixes, err := SelectInterface(interfaceName)
	if err != nil {
		return err
	}
	group := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	connection, err := net.ListenMulticastUDP("udp4", &iface, group)
	if err != nil {
		return fmt.Errorf("open tablet mDNS discovery on %q: %w", iface.Name, err)
	}
	defer connection.Close()
	packets := ipv4.NewPacketConn(connection)
	if err := packets.SetMulticastInterface(&iface); err != nil {
		return fmt.Errorf("select mDNS query interface: %w", err)
	}
	if err := packets.SetMulticastTTL(255); err != nil {
		return fmt.Errorf("set link-local mDNS query TTL: %w", err)
	}
	if err := packets.SetControlMessage(ipv4.FlagInterface|ipv4.FlagTTL, true); err != nil {
		return fmt.Errorf("enable mDNS receiving-interface/TTL checks: %w", err)
	}
	query := new(dns.Msg)
	query.Question = []dns.Question{{Name: TabletService + ".local.", Qtype: dns.TypePTR, Qclass: dns.ClassINET}}
	queryData, err := query.Pack()
	if err != nil {
		return fmt.Errorf("encode tablet mDNS query: %w", err)
	}
	browseCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cache := newPacketCache()
	buffer := make([]byte, 4097)
	nextQuery := time.Time{}
	checkInterface := func() error {
		current, currentPrefixes, err := SelectInterface(iface.Name)
		if err != nil {
			return fmt.Errorf("selected LAN changed or disappeared; restart discovery: %w", err)
		}
		return validateInterfaceSnapshot(iface, prefixes, current, currentPrefixes)
	}
	for {
		if err := browseCtx.Err(); err != nil {
			return err
		}
		now := time.Now()
		if !now.Before(nextQuery) {
			if err := checkInterface(); err != nil {
				return err
			}
			if _, err := packets.WriteTo(queryData, &ipv4.ControlMessage{IfIndex: iface.Index}, group); err != nil {
				return fmt.Errorf("send tablet mDNS query on selected interface: %w", err)
			}
			nextQuery = now.Add(3 * time.Second)
		}
		deadline := minTime(now.Add(time.Second), nextQuery)
		if end, ok := browseCtx.Deadline(); ok {
			deadline = minTime(deadline, end)
		}
		if err := connection.SetReadDeadline(deadline); err != nil {
			return fmt.Errorf("set tablet discovery read deadline: %w", err)
		}
		n, control, source, err := packets.ReadFrom(buffer)
		if err != nil {
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				continue
			}
			return fmt.Errorf("receive tablet mDNS response: %w", err)
		}
		peer, ok := source.(*net.UDPAddr)
		if !ok || peer.Port != 5353 || control == nil || control.IfIndex != iface.Index || control.TTL != 255 {
			rejected(fmt.Errorf("mDNS response did not originate on the selected link"))
			continue
		}
		if err := checkInterface(); err != nil {
			return err
		}
		address, ok := netip.AddrFromSlice(peer.IP)
		if !ok || !onLinkIPv4(address.Unmap(), prefixes) {
			rejected(fmt.Errorf("mDNS responder is not a peer on the selected IPv4 LAN"))
			continue
		}
		candidates, err := cache.consume(buffer[:n], time.Now(), prefixes)
		if err != nil {
			if errors.Is(err, ErrDiscoveryCapacity) {
				return err
			}
			rejected(err)
			continue
		}
		for _, candidate := range candidates {
			found(candidate)
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func validateInterfaceSnapshot(before net.Interface, prefixes []netip.Prefix, after net.Interface, current []netip.Prefix) error {
	changed := before.Index != after.Index || before.Name != after.Name || len(prefixes) != len(current)
	if !changed {
		for _, prefix := range prefixes {
			if !slices.Contains(current, prefix) {
				changed = true
				break
			}
		}
	}
	if changed {
		return errors.New("selected LAN interface generation or IPv4 addresses changed; restart discovery on the new LAN")
	}
	return nil
}
