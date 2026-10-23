package main

import (
	"errors"
	"io"
	"net"
	"sync"

	"drawsplay/host/internal/wifilan"
)

// keyframeRequestLine is how a tablet keyframe request reaches the stats
// reader (sessionMonitor.runStatsReader).
const keyframeRequestLine = "{\"type\":\"keyframe\"}\n"

var errWiFiVideoUpstream = errors.New("unexpected Wi-Fi video upstream record type, epoch or sequence; reconnect video")

// wifiVideoConn gives the existing video session a net.Conn whose writes are
// translated by the bridge. Without negotiated stats Read blocks until Close,
// so adaptive-bitrate feedback stays idle. With FeatureVideoStats each
// upstream subtype 8 record is returned as one newline-terminated JSON line,
// which is exactly what sessionMonitor.runStatsReader parses on USB.
type wifiVideoConn struct {
	net.Conn
	out  io.Writer
	done chan struct{}
	once sync.Once

	reader  *wifilan.FrameReader
	guard   *wifilan.SequenceGuard
	epoch   uint64
	buffer  []byte
	pending []byte

	// maxFPS is the negotiated Hello frame-rate ceiling (0 = none known).
	maxFPS int
}

func newWiFiVideoConn(conn net.Conn, out io.Writer) *wifiVideoConn {
	return &wifiVideoConn{Conn: conn, out: out, done: make(chan struct{})}
}

// newWiFiVideoConnWithStats reads tablet stats records; the attach record
// already used upstream sequence 1, so records start at sequence 2.
func newWiFiVideoConnWithStats(conn net.Conn, out io.Writer, epoch uint64) *wifiVideoConn {
	c := newWiFiVideoConn(conn, out)
	guard, err := wifilan.NewSequenceGuard(epoch)
	if err != nil {
		return c
	}
	_ = guard.Accept(wifilan.FrameHeader{Epoch: epoch, Sequence: 1})
	c.reader, c.guard, c.epoch = wifilan.NewFrameReader(conn), guard, epoch
	c.buffer = make([]byte, wifilan.MaxControlSize+1)
	return c
}

func (c *wifiVideoConn) Write(p []byte) (int, error) { return c.out.Write(p) }

func (c *wifiVideoConn) Read(p []byte) (int, error) {
	if c.reader == nil {
		<-c.done
		return 0, io.EOF
	}
	for len(c.pending) == 0 {
		header, err := c.reader.ReadHeader()
		if err != nil {
			return 0, err
		}
		if header.Type != wifilan.FrameControl || header.Epoch != c.epoch {
			return 0, errWiFiVideoUpstream
		}
		if err := c.guard.Accept(header); err != nil {
			return 0, err
		}
		raw, err := c.reader.ReadPayload(c.buffer[:wifilan.MaxControlSize])
		if err != nil {
			return 0, err
		}
		if len(raw) == 4 && [4]byte(raw) == wifilan.KeyframeRequest {
			c.pending = append(c.buffer[:0], keyframeRequestLine...)
			continue
		}
		body, err := wifilan.DecodeVideoStats(raw)
		if err != nil {
			return 0, err
		}
		// body aliases buffer[4:]; the spare byte holds the line terminator.
		c.pending = c.buffer[4 : 4+len(body)+1]
		c.pending[len(body)] = '\n'
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *wifiVideoConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
