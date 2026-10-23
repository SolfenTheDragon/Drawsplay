package main

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestWiFiVideoConnWritesGoThroughBridgeAndReadBlocksUntilClose(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	var got []byte
	sink := writerFunc(func(p []byte) (int, error) { got = append(got, p...); return len(p), nil })
	c := newWiFiVideoConn(a, sink)
	if n, err := c.Write([]byte{1, 2, 3}); err != nil || n != 3 || string(got) != "\x01\x02\x03" {
		t.Fatalf("write %d %v %v", n, err, got)
	}
	res := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 4)); res <- err }()
	select {
	case err := <-res:
		t.Fatalf("read returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	c.Close()
	if err := <-res; err != io.EOF {
		t.Fatalf("read after close: %v", err)
	}
	if _, err := b.Write([]byte{1}); err == nil {
		t.Fatal("underlying conn not closed")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
