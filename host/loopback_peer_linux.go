package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// loopbackPeerIsOtherUser reports whether the client end of a loopback TCP
// connection belongs to a different, non-root user. It finds the client
// socket in /proc/net/tcp{,6}: the row whose local address is the
// connection's remote address and whose remote address is our local one.
func loopbackPeerIsOtherUser(c net.Conn) (bool, error) {
	remote, err1 := netip.ParseAddrPort(c.RemoteAddr().String())
	local, err2 := netip.ParseAddrPort(c.LocalAddr().String())
	if err1 != nil || err2 != nil {
		return false, errors.New("not a TCP connection")
	}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(table)
		if err != nil {
			continue
		}
		uid, found := findSocketOwner(bufio.NewScanner(f), remote, local)
		f.Close()
		if found {
			return uid != os.Geteuid() && uid != 0, nil
		}
	}
	return false, fmt.Errorf("socket %s not found in /proc/net/tcp", remote)
}

// findSocketOwner scans /proc/net/tcp-format rows for the socket with the
// given local and remote endpoints and returns its uid.
func findSocketOwner(s *bufio.Scanner, local, remote netip.AddrPort) (int, bool) {
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) < 8 || f[0] == "sl" {
			continue
		}
		l, ok1 := parseProcAddr(f[1])
		r, ok2 := parseProcAddr(f[2])
		if !ok1 || !ok2 || l != local || r != remote {
			continue
		}
		uid, err := strconv.Atoi(f[7])
		if err != nil {
			return 0, false
		}
		return uid, true
	}
	return 0, false
}

// parseProcAddr decodes "0100007F:6AF7": the address as host-order 32-bit
// words (little-endian on the machines DrawSplay runs on), the port big-endian.
func parseProcAddr(s string) (netip.AddrPort, bool) {
	a, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(a)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), true
}
