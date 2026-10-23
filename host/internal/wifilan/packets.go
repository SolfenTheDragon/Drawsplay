package wifilan

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/miekg/dns"
)

var ErrDiscoveryCapacity = errors.New("Wi-Fi discovery cache capacity reached; stop and check LAN traffic")

type serviceRecord struct {
	name, host                   string
	port                         int
	text                         []string
	ptrUntil, srvUntil, txtUntil time.Time
}

type hostRecord struct {
	addresses map[netip.Addr]time.Time
}

type packetCache struct {
	services map[string]*serviceRecord
	hosts    map[string]*hostRecord
}

func newPacketCache() *packetCache {
	return &packetCache{
		services: make(map[string]*serviceRecord),
		hosts:    make(map[string]*hostRecord),
	}
}

func (c *packetCache) expire(now time.Time) {
	for name, service := range c.services {
		if !service.ptrUntil.After(now) && !service.srvUntil.After(now) && !service.txtUntil.After(now) {
			delete(c.services, name)
		}
	}
	for name, host := range c.hosts {
		for address, until := range host.addresses {
			if !until.After(now) {
				delete(host.addresses, address)
			}
		}
		if len(host.addresses) == 0 {
			delete(c.hosts, name)
		}
	}
}

func (c *packetCache) service(name string) (*serviceRecord, error) {
	key := strings.ToLower(name)
	if service := c.services[key]; service != nil {
		return service, nil
	}
	if len(c.services) >= 64 {
		return nil, ErrDiscoveryCapacity
	}
	suffix := "." + TabletService + ".local."
	if !strings.HasSuffix(key, suffix) {
		return nil, fmt.Errorf("record does not name a tablet service")
	}
	// DNS presentation form escapes spaces, dots, backslashes and UTF-8 bytes.
	// Recover the single wire label before showing it or comparing UI selection.
	if len(dns.SplitDomainName(name)) != 4 {
		return nil, fmt.Errorf("tablet service instance must be one DNS label")
	}
	var wire [256]byte
	if _, err := dns.PackDomainName(name, wire[:], 0, nil, false); err != nil || wire[0] == 0 || wire[0] > 63 {
		return nil, fmt.Errorf("invalid tablet service instance encoding")
	}
	service := &serviceRecord{name: string(wire[1 : 1+int(wire[0])])}
	c.services[key] = service
	return service, nil
}

func (c *packetCache) consume(data []byte, now time.Time, prefixes []netip.Prefix) ([]Candidate, error) {
	if len(data) < 12 || len(data) > 4096 {
		return nil, fmt.Errorf("discovery DNS packet must be 12..4096 bytes")
	}
	count := 0
	for offset := 4; offset < 12; offset += 2 {
		count += int(binary.BigEndian.Uint16(data[offset : offset+2]))
	}
	if count > 64 {
		return nil, fmt.Errorf("discovery DNS packet exceeds 64 records/questions")
	}
	message := new(dns.Msg)
	if err := message.Unpack(data); err != nil {
		return nil, fmt.Errorf("decode discovery DNS packet: %w", err)
	}
	if !message.Response {
		return nil, nil
	}
	if message.Truncated || message.Opcode != dns.OpcodeQuery || message.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("unsupported or truncated discovery DNS response")
	}
	c.expire(now)
	records := append(message.Answer, message.Extra...)
	for _, record := range records {
		header := record.Header()
		if header.Class&0x7fff != dns.ClassINET {
			continue
		}
		ttl := min(header.Ttl, 120)
		until := now.Add(time.Duration(ttl) * time.Second)
		switch record := record.(type) {
		case *dns.PTR:
			if !strings.EqualFold(header.Name, TabletService+".local.") {
				continue
			}
			if ttl == 0 {
				delete(c.services, strings.ToLower(record.Ptr))
				continue
			}
			service, err := c.service(record.Ptr)
			if err != nil {
				return nil, err
			}
			service.ptrUntil = until
		case *dns.SRV:
			if !strings.HasSuffix(strings.ToLower(header.Name), "."+TabletService+".local.") {
				continue
			}
			service, err := c.service(header.Name)
			if err != nil {
				return nil, err
			}
			service.port, service.host, service.srvUntil = int(record.Port), strings.ToLower(record.Target), until
		case *dns.TXT:
			if !strings.HasSuffix(strings.ToLower(header.Name), "."+TabletService+".local.") {
				continue
			}
			if len(record.Txt) > 16 {
				return nil, fmt.Errorf("discovery TXT exceeds 16 attributes")
			}
			size := 0
			for _, value := range record.Txt {
				size += len(value)
			}
			if size > 1024 {
				return nil, fmt.Errorf("discovery TXT exceeds 1024 bytes")
			}
			service, err := c.service(header.Name)
			if err != nil {
				return nil, err
			}
			service.text, service.txtUntil = record.Txt, until
		case *dns.A:
			address, ok := netip.AddrFromSlice(record.A)
			if !ok {
				return nil, fmt.Errorf("invalid discovery A record")
			}
			address = address.Unmap()
			key := strings.ToLower(header.Name)
			host := c.hosts[key]
			if ttl == 0 {
				if host != nil {
					delete(host.addresses, address)
				}
				continue
			}
			if host == nil {
				if len(c.hosts) >= 64 {
					return nil, ErrDiscoveryCapacity
				}
				host = &hostRecord{addresses: make(map[netip.Addr]time.Time)}
				c.hosts[key] = host
			}
			if _, exists := host.addresses[address]; !exists && len(host.addresses) >= 8 {
				return nil, fmt.Errorf("discovery hostname exceeds eight addresses")
			}
			host.addresses[address] = until
		}
	}
	var candidates []Candidate
	for key, service := range c.services {
		if !service.ptrUntil.After(now) || !service.srvUntil.After(now) || !service.txtUntil.After(now) {
			continue
		}
		host := c.hosts[service.host]
		if host == nil {
			continue
		}
		entry := zeroconf.NewServiceEntry(service.name, TabletService, "local.")
		entry.Port, entry.Text, entry.TTL = service.port, service.text, 1
		for address, until := range host.addresses {
			if until.After(now) {
				raw := address.As4()
				entry.AddrIPv4 = append(entry.AddrIPv4, net.IP(raw[:]))
			}
		}
		candidate, err := ParseCandidate(entry, prefixes)
		if err != nil {
			delete(c.services, key)
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}
