package main

import (
	"net"
	"testing"
	"time"
)

func TestWiFiConnListenerDeliversPushedConnsThenCloses(t *testing.T) {
	ln := newWiFiConnListener()
	a, b := net.Pipe()
	defer b.Close()
	if err := ln.Push(a); err != nil {
		t.Fatal(err)
	}
	got, err := ln.Accept()
	if err != nil || got != a {
		t.Fatalf("accept %v %v", got, err)
	}
	blocked := make(chan error, 1)
	go func() { _, err := ln.Accept(); blocked <- err }()
	time.Sleep(20 * time.Millisecond)
	ln.Close()
	if err := <-blocked; err != net.ErrClosed {
		t.Fatalf("got %v", err)
	}
	c, d := net.Pipe()
	defer d.Close()
	if err := ln.Push(c); err == nil {
		t.Fatal("push after close accepted")
	}
	if _, err := c.Write([]byte{1}); err == nil {
		t.Fatal("rejected conn left open")
	}
	ln.Close()
}

func TestWiFiConnListenerPushNeverBlocksWhenFull(t *testing.T) {
	ln := newWiFiConnListener()
	defer ln.Close()
	var conns []net.Conn
	for i := 0; i < 8; i++ {
		a, b := net.Pipe()
		defer b.Close()
		conns = append(conns, a)
		if err := ln.Push(a); err != nil {
			return
		}
	}
	t.Fatal("expected full-queue rejection")
}
