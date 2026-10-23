package main

import (
	"io"
	"net"
	"sync"
)

// wifiVideoConn gives the existing video session a net.Conn whose writes are
// translated by the bridge. The first Wi-Fi release carries no upstream decode
// stats, so Read just blocks until Close and adaptive-bitrate feedback stays idle.
type wifiVideoConn struct {
	net.Conn
	out  io.Writer
	done chan struct{}
	once sync.Once
}

func newWiFiVideoConn(conn net.Conn, out io.Writer) *wifiVideoConn {
	return &wifiVideoConn{Conn: conn, out: out, done: make(chan struct{})}
}

func (c *wifiVideoConn) Write(p []byte) (int, error) { return c.out.Write(p) }

func (c *wifiVideoConn) Read([]byte) (int, error) {
	<-c.done
	return 0, io.EOF
}

func (c *wifiVideoConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
