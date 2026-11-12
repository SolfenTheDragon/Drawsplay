package wifilan

import "net"

// ServiceEntry is one resolved DNS-SD service instance, as assembled from
// mDNS answers in packets.go. (It replaces grandcat/zeroconf's type of the
// same name: that library is unmaintained and only this data type was used;
// the packet parsing is DrawSplay's own, on miekg/dns.)
type ServiceEntry struct {
	Instance string // e.g. "Galaxy Tab A8"
	Service  string // e.g. "_drawsplay-tablet._tcp"
	Domain   string // e.g. "local."
	HostName string
	Port     int
	Text     []string // TXT record strings
	TTL      uint32   // of the SRV record
	AddrIPv4 []net.IP
	AddrIPv6 []net.IP
}

// NewServiceEntry returns an entry with only its identity filled in.
func NewServiceEntry(instance, service, domain string) *ServiceEntry {
	return &ServiceEntry{Instance: instance, Service: service, Domain: domain}
}
