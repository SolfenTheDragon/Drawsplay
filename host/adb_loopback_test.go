package main

import (
	"net"
	"testing"
	"time"
)

func TestSameUserListenerAcceptsOwnUserAndClosesSessionsOnClose(t *testing.T) {
	ln, err := listenSameUser("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("own-user loopback connection was not accepted")
	}
	if underlyingTCPConn(server) == nil {
		t.Fatal("tracked connection must still expose the TCP socket")
	}
	// Turning the adb tunnel off closes the listener and the live session.
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("session survived closing the listener")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("session still open after closing the listener")
	}
}
