package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const tcpTableOwnerPIDAll = 5

// loopbackPeerIsOtherUser reports whether the client end of a loopback TCP
// connection belongs to a process of a different user (SYSTEM counts as the
// same: it can act as anyone). The owning process comes from the IPv4 TCP
// table (GetExtendedTcpTable), its user from the process token.
func loopbackPeerIsOtherUser(c net.Conn) (bool, error) {
	remote, err1 := netip.ParseAddrPort(c.RemoteAddr().String())
	local, err2 := netip.ParseAddrPort(c.LocalAddr().String())
	if err1 != nil || err2 != nil || !remote.Addr().Unmap().Is4() {
		return false, errors.New("not an IPv4 TCP connection")
	}
	pid, err := tcpOwnerPID(remote, local)
	if err != nil {
		return false, err
	}
	if pid == 0 || pid == 4 {
		return false, nil // System Idle / System
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		// Another user's elevated or protected process cannot be opened
		// from here; that is itself "not us".
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return true, nil
		}
		return false, err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return true, nil
		}
		return false, err
	}
	defer tok.Close()
	peer, err := tok.GetTokenUser()
	if err != nil {
		return false, err
	}
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	if windows.EqualSid(peer.User.Sid, me.User.Sid) || peer.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return false, nil
	}
	return true, nil
}

// tcpOwnerPID finds the process owning the IPv4 socket local -> remote.
func tcpOwnerPID(local, remote netip.AddrPort) (uint32, error) {
	var size uint32
	for attempt := 0; attempt < 4; attempt++ {
		var buf []byte
		var p uintptr
		if size > 0 {
			buf = make([]byte, size)
			p = uintptr(unsafe.Pointer(&buf[0]))
		}
		r, _, _ := procGetExtendedTcpTable.Call(p, uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, tcpTableOwnerPIDAll, 0)
		if r == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || buf == nil {
			continue
		}
		if r != 0 {
			return 0, fmt.Errorf("GetExtendedTcpTable: %w", windows.Errno(r))
		}
		return findOwnerPID(buf, local, remote)
	}
	return 0, errors.New("GetExtendedTcpTable: table kept growing")
}

// findOwnerPID walks a MIB_TCPTABLE_OWNER_PID: a u32 row count, then rows of
// six u32s (state, local addr, local port, remote addr, remote port, pid).
// Addresses are in network byte order as stored; ports sit in the low 16
// bits in network byte order.
func findOwnerPID(buf []byte, local, remote netip.AddrPort) (uint32, error) {
	if len(buf) < 4 {
		return 0, errors.New("short TCP table")
	}
	n := int(binary.LittleEndian.Uint32(buf))
	const row = 24
	for i := 0; i < n && 4+(i+1)*row <= len(buf); i++ {
		r := buf[4+i*row : 4+(i+1)*row]
		la := netip.AddrFrom4([4]byte(r[4:8]))
		lp := binary.BigEndian.Uint16(r[8:10])
		ra := netip.AddrFrom4([4]byte(r[12:16]))
		rp := binary.BigEndian.Uint16(r[16:18])
		if netip.AddrPortFrom(la, lp) == local && netip.AddrPortFrom(ra, rp) == remote {
			return binary.LittleEndian.Uint32(r[20:24]), nil
		}
	}
	return 0, fmt.Errorf("socket %s not found in the TCP table", local)
}
