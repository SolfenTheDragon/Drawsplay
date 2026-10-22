// Package wifilan handles untrusted tablet discovery independently of USB.
package wifilan

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/grandcat/zeroconf"
)

const TabletService = "_drawsplay-tablet._tcp"

// Candidate is discovery metadata, never evidence of identity or pairing.
type Candidate struct {
	Name      string       `json:"name"`
	Addresses []netip.Addr `json:"addresses"`
	InputPort int          `json:"inputPort"`
	VideoPort int          `json:"videoPort"`
}

func ParseCandidate(entry *zeroconf.ServiceEntry, prefixes []netip.Prefix) (Candidate, error) {
	if entry == nil {
		return Candidate{}, fmt.Errorf("missing tablet advertisement")
	}
	if strings.TrimSuffix(entry.Service, ".") != TabletService ||
		strings.TrimSuffix(entry.Domain, ".") != "local" {
		return Candidate{}, fmt.Errorf("not a local tablet service")
	}
	if entry.TTL == 0 || len(entry.Instance) == 0 || len(entry.Instance) > 63 ||
		!utf8.ValidString(entry.Instance) || strings.IndexFunc(entry.Instance, unicode.IsControl) >= 0 {
		return Candidate{}, fmt.Errorf("expired advertisement or invalid tablet name")
	}
	if len(entry.Text) > 16 || len(entry.AddrIPv4) > 8 || len(entry.AddrIPv6) > 8 {
		return Candidate{}, fmt.Errorf("tablet advertisement exceeds metadata bounds")
	}
	attributes := make(map[string]string, len(entry.Text))
	total := 0
	for _, text := range entry.Text {
		total += len(text)
		if len(text) > 255 || total > 1024 {
			return Candidate{}, fmt.Errorf("tablet TXT metadata exceeds byte bounds")
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok || key == "" {
			return Candidate{}, fmt.Errorf("malformed tablet TXT attribute")
		}
		key = strings.ToLower(key)
		if _, exists := attributes[key]; exists {
			return Candidate{}, fmt.Errorf("duplicate tablet TXT attribute %q", key)
		}
		attributes[key] = value
	}
	if attributes["v"] != "2" {
		return Candidate{}, fmt.Errorf("tablet protocol version is not supported; require v=2")
	}
	videoPort, err := strconv.Atoi(attributes["videoport"])
	if err != nil || videoPort < 1024 || videoPort > 65535 ||
		entry.Port < 1024 || entry.Port > 65535 || entry.Port == videoPort {
		return Candidate{}, fmt.Errorf("tablet requires distinct input/video ports in 1024..65535")
	}
	candidate := Candidate{Name: entry.Instance, InputPort: entry.Port, VideoPort: videoPort}
	for _, ip := range entry.AddrIPv4 {
		address, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		address = address.Unmap()
		if !onLinkIPv4(address, prefixes) {
			continue
		}
		duplicate := false
		for _, previous := range candidate.Addresses {
			if previous == address {
				duplicate = true
				break
			}
		}
		if !duplicate {
			candidate.Addresses = append(candidate.Addresses, address)
		}
	}
	if len(candidate.Addresses) == 0 {
		return Candidate{}, fmt.Errorf("tablet has no usable IPv4 address on the selected LAN")
	}
	slices.SortFunc(candidate.Addresses, netip.Addr.Compare)
	return candidate, nil
}

func onLinkIPv4(address netip.Addr, prefixes []netip.Prefix) bool {
	if !address.Is4() || !address.IsGlobalUnicast() || address.IsLoopback() {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.IsValid() && prefix.Addr() == address {
			return false
		}
	}
	for _, prefix := range prefixes {
		if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Bits() < 1 ||
			!prefix.Contains(address) {
			continue
		}
		network := prefix.Masked()
		if prefix.Bits() <= 30 {
			raw := address.As4()
			base := network.Addr().As4()
			value := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
			start := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
			broadcast := start | (^uint32(0) >> prefix.Bits())
			if value == start || value == broadcast {
				continue
			}
		}
		return true
	}
	return false
}
