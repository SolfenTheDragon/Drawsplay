package main

import (
	"bufio"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestFindSocketOwnerParsesProcNetTCP(t *testing.T) {
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:6AF7 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0000000000000000 100 0 0 10 0
   1: 0100007F:D431 0100007F:6AF7 01 00000000:00000000 00:00000000 00000000  1001        0 2 1 0000000000000000 20 4 30 10 -1
`
	client := netip.MustParseAddrPort("127.0.0.1:54321")
	server := netip.MustParseAddrPort("127.0.0.1:27383")
	uid, ok := findSocketOwner(bufio.NewScanner(strings.NewReader(table)), client, server)
	if !ok || uid != 1001 {
		t.Fatalf("uid=%d ok=%v, want 1001", uid, ok)
	}
	if _, ok := findSocketOwner(bufio.NewScanner(strings.NewReader(table)), server, client); ok {
		t.Fatal("matched the wrong direction")
	}
}

func TestParseProcAddrIPv6Loopback(t *testing.T) {
	got, ok := parseProcAddr("00000000000000000000000001000000:0050")
	if !ok || got != netip.MustParseAddrPort("[::1]:80") {
		t.Fatalf("got %v ok=%v", got, ok)
	}
}

func TestOwnLoopbackConnectionIsSameUser(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err == nil {
			defer c.Close()
			buf := make([]byte, 1)
			_, _ = c.Read(buf)
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	other, err := loopbackPeerIsOtherUser(c)
	if err != nil || other {
		t.Fatalf("own connection: other=%v err=%v", other, err)
	}
}
