package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// AOA link framing. The accessory bulk pipe is a single byte stream that
// carries the two existing DrawSplay channels (input and video) as independent
// virtual connections, so the established input and video handlers are reused
// unchanged.
//
// Frame header (big-endian, 5 bytes): type(1) id(2) len(2), followed by len payload bytes.
const (
	aoaFrameHeader     = 5
	aoaMaxFramePayload = 16000 // keeps a whole frame within one 16 KiB accessory transfer

	aoaFrameHello byte = 1 // id=0, payload = aoaHelloPayload; tablet sends first, host answers
	aoaFrameOpen  byte = 2 // tablet -> host, payload = kind
	aoaFrameData  byte = 3
	aoaFrameClose byte = 4

	aoaKindInput byte = 1
	aoaKindVideo byte = 2

	aoaLinkVersion = 1

	aoaInboundBufferLimit = 4 * 1024 * 1024
	aoaHelloWait          = 90 * time.Second
)

var aoaHelloPayload = []byte{'D', 'S', 'A', 'O', 'A', aoaLinkVersion}

type aoaAddr struct{ id uint16 }

func (aoaAddr) Network() string  { return "usb-aoa" }
func (a aoaAddr) String() string { return fmt.Sprintf("usb-aoa#%d", a.id) }

// aoaLink multiplexes virtual connections over one accessory pipe.
type aoaLink struct {
	pipe aoaPipe
	// writeMu serializes frame writes. It is a channel so writers can honor deadlines.
	writeSem chan struct{}

	mu     sync.Mutex
	conns  map[uint16]*aoaConn
	closed bool
	done   chan struct{}
	err    error
}

func newAOALink(pipe aoaPipe) *aoaLink {
	return &aoaLink{
		pipe:     pipe,
		writeSem: make(chan struct{}, 1),
		conns:    make(map[uint16]*aoaConn),
		done:     make(chan struct{}),
	}
}

func (l *aoaLink) shutdown(err error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.err = err
	conns := l.conns
	l.conns = map[uint16]*aoaConn{}
	close(l.done)
	l.mu.Unlock()
	_ = l.pipe.Close()
	for _, c := range conns {
		c.remoteClose()
	}
}

func (l *aoaLink) writeFrame(typ byte, id uint16, payload []byte, deadline time.Time) error {
	if len(payload) > aoaMaxFramePayload {
		return fmt.Errorf("aoa frame payload too large: %d", len(payload))
	}
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return os.ErrDeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case l.writeSem <- struct{}{}:
	case <-l.done:
		return io.ErrClosedPipe
	case <-timeout:
		return os.ErrDeadlineExceeded
	}
	defer func() { <-l.writeSem }()

	buf := make([]byte, aoaFrameHeader+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint16(buf[1:3], id)
	binary.BigEndian.PutUint16(buf[3:5], uint16(len(payload)))
	copy(buf[aoaFrameHeader:], payload)
	if _, err := l.pipe.Write(buf); err != nil {
		l.shutdown(err)
		return err
	}
	return nil
}

func readAOAFrame(r io.Reader) (typ byte, id uint16, payload []byte, err error) {
	var hdr [aoaFrameHeader]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return
	}
	typ = hdr[0]
	id = binary.BigEndian.Uint16(hdr[1:3])
	n := int(binary.BigEndian.Uint16(hdr[3:5]))
	if n > aoaMaxFramePayload {
		err = fmt.Errorf("aoa frame payload too large: %d", n)
		return
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return
}

// serve performs the hello exchange, then dispatches inbound frames until the
// pipe fails or ctx ends. onOpen is called for each tablet-initiated virtual
// connection and must not block.
func (l *aoaLink) serve(ctx context.Context, onOpen func(kind byte, conn *aoaConn)) error {
	go func() {
		select {
		case <-ctx.Done():
			l.shutdown(ctx.Err())
		case <-l.done:
		}
	}()

	// The tablet speaks first: the accessory endpoints only service transfers
	// once the Android app has opened the accessory, which may wait on the
	// user's USB permission dialog.
	helloTimer := time.AfterFunc(aoaHelloWait, func() { l.shutdown(errors.New("timed out waiting for the tablet app to open the accessory")) })
	defer helloTimer.Stop()

	r := bufio.NewReaderSize(l.pipe, 64*1024)
	typ, _, payload, err := readAOAFrame(r)
	if err != nil {
		l.shutdown(err)
		return err
	}
	if typ != aoaFrameHello || len(payload) < len(aoaHelloPayload) || string(payload[:5]) != "DSAOA" {
		err = errors.New("unexpected aoa hello from tablet")
		l.shutdown(err)
		return err
	}
	if payload[5] != aoaLinkVersion {
		err = fmt.Errorf("unsupported aoa link version %d", payload[5])
		l.shutdown(err)
		return err
	}
	if err := l.writeFrame(aoaFrameHello, 0, aoaHelloPayload, time.Time{}); err != nil {
		return err
	}
	helloTimer.Stop()

	for {
		typ, id, payload, err := readAOAFrame(r)
		if err != nil {
			l.shutdown(err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		switch typ {
		case aoaFrameOpen:
			if len(payload) < 1 {
				continue
			}
			c := newAOAConn(l, id)
			l.mu.Lock()
			if l.closed {
				l.mu.Unlock()
				continue
			}
			if old := l.conns[id]; old != nil {
				old.remoteClose()
			}
			l.conns[id] = c
			l.mu.Unlock()
			onOpen(payload[0], c)
		case aoaFrameData:
			l.mu.Lock()
			c := l.conns[id]
			l.mu.Unlock()
			if c != nil && !c.feed(payload) {
				c.Close()
			}
		case aoaFrameClose:
			l.mu.Lock()
			c := l.conns[id]
			delete(l.conns, id)
			l.mu.Unlock()
			if c != nil {
				c.remoteClose()
			}
		}
	}
}

func (l *aoaLink) forget(id uint16) {
	l.mu.Lock()
	delete(l.conns, id)
	l.mu.Unlock()
}

// aoaConn is one virtual connection over the link and implements net.Conn.
type aoaConn struct {
	link *aoaLink
	id   uint16

	mu            sync.Mutex
	cond          *sync.Cond
	buf           []byte
	remoteClosed  bool
	localClosed   bool
	readDeadline  time.Time
	writeDeadline time.Time
	readTimer     *time.Timer
}

func newAOAConn(l *aoaLink, id uint16) *aoaConn {
	c := &aoaConn{link: l, id: id}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// feed appends inbound data; it returns false if the peer overran the buffer limit.
func (c *aoaConn) feed(p []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.localClosed || c.remoteClosed {
		return true
	}
	if len(c.buf)+len(p) > aoaInboundBufferLimit {
		return false
	}
	c.buf = append(c.buf, p...)
	c.cond.Broadcast()
	return true
}

func (c *aoaConn) remoteClose() {
	c.mu.Lock()
	c.remoteClosed = true
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *aoaConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.localClosed {
			return 0, net.ErrClosed
		}
		if len(c.buf) > 0 {
			n := copy(p, c.buf)
			c.buf = c.buf[n:]
			if len(c.buf) == 0 {
				c.buf = nil
			}
			return n, nil
		}
		if c.remoteClosed {
			return 0, io.EOF
		}
		if !c.readDeadline.IsZero() && !time.Now().Before(c.readDeadline) {
			return 0, os.ErrDeadlineExceeded
		}
		c.cond.Wait()
	}
}

func (c *aoaConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	if c.localClosed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	if c.remoteClosed {
		c.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	deadline := c.writeDeadline
	c.mu.Unlock()

	written := 0
	for written < len(p) {
		end := written + aoaMaxFramePayload
		if end > len(p) {
			end = len(p)
		}
		if err := c.link.writeFrame(aoaFrameData, c.id, p[written:end], deadline); err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

func (c *aoaConn) Close() error {
	c.mu.Lock()
	if c.localClosed {
		c.mu.Unlock()
		return nil
	}
	c.localClosed = true
	if c.readTimer != nil {
		c.readTimer.Stop()
	}
	c.cond.Broadcast()
	remoteAlready := c.remoteClosed
	c.mu.Unlock()
	c.link.forget(c.id)
	if !remoteAlready {
		_ = c.link.writeFrame(aoaFrameClose, c.id, nil, time.Now().Add(time.Second))
	}
	return nil
}

func (c *aoaConn) LocalAddr() net.Addr  { return aoaAddr{c.id} }
func (c *aoaConn) RemoteAddr() net.Addr { return aoaAddr{c.id} }

func (c *aoaConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}

func (c *aoaConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	if c.readTimer != nil {
		c.readTimer.Stop()
		c.readTimer = nil
	}
	if !t.IsZero() {
		c.readTimer = time.AfterFunc(time.Until(t), func() {
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
		})
	}
	c.cond.Broadcast()
	return nil
}

func (c *aoaConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

// aoaListener adapts tablet-opened virtual connections to net.Listener so the
// existing input listener loop can serve them.
type aoaListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newAOAListener() *aoaListener {
	return &aoaListener{ch: make(chan net.Conn, 4), done: make(chan struct{})}
}

func (l *aoaListener) offer(c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-l.done:
		return false
	default:
		return false
	}
}

func (l *aoaListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *aoaListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *aoaListener) Addr() net.Addr { return aoaAddr{} }
