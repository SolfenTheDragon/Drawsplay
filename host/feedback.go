package main

// Video session monitoring (milestone M4).
//
// The video TCP socket is duplex: while the host streams H.264 downstream,
// the client reports decode statistics upstream as newline-delimited JSON
// (clientStatsMsg in protocol.go). sessionMonitor consumes that feedback and
// watches host-side write stalls, condensing both into two signals for the
// session loop:
//
//	congestion — the link or the decoder is not keeping up; the session
//	             loop reacts by stepping the bitrate ladder down.
//	client gone — the upstream read hit EOF/error, the fastest reliable
//	              dead-peer signal (write timeouts take seconds longer).
//
// The monitor also keeps the latest stats snapshot for the periodic
// session summary log.

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// congestQueueDepth: a client decode queue at or above this depth means
	// frames are arriving faster than they are decoded.
	congestQueueDepth = 8
	// slowWriteThreshold: a single blocking send slower than this means the
	// socket buffer is full — the link is congested.
	slowWriteThreshold = 300 * time.Millisecond
	// videoWriteTimeout: writes blocked this long mean the peer is dead.
	videoWriteTimeout = 5 * time.Second
	// maxStatsLineBytes bounds upstream line size (stats are ~200 bytes).
	maxStatsLineBytes = 4 * 1024
)

// sessionMonitor aggregates congestion and liveness signals for one video
// session. All fields are goroutine-safe (reader goroutine + session loop).
type sessionMonitor struct {
	congested   atomic.Bool
	linkStalled atomic.Bool  // a slow socket write raised congested (link evidence)
	bytesSent   atomic.Int64 // total bytes written to the client
	clientGone  atomic.Bool
	lastStats   atomic.Pointer[clientStatsMsg]
	staleFrames atomic.Int64 // client stale-frame skips not yet logged
}

var latestClientVideoStats = struct {
	sync.RWMutex
	value *clientStatsMsg
}{}

// runStatsReader consumes upstream stats lines until the connection dies.
// Run as a goroutine; it owns the read half of conn.
func (m *sessionMonitor) runStatsReader(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 512), maxStatsLineBytes)
	for scanner.Scan() {
		var stats clientStatsMsg
		if err := json.Unmarshal(scanner.Bytes(), &stats); err != nil || stats.Type != "stats" {
			continue // tolerate unknown upstream traffic
		}
		m.lastStats.Store(&stats)
		publishLatestClientVideoStats(&stats)
		if stats.StaleFrames > 0 {
			m.staleFrames.Add(stats.StaleFrames)
		}
		if decodeQueueCongested(&stats) || stats.FrameDrops > 0 {
			m.congested.Store(true)
		}
	}
	m.clientGone.Store(true)
}

// decodeQueueCongested reports whether a client's decode queue says frames
// arrive faster than it can decode them. A deep queue while the client is
// skipping stale frames is the backlog of a stall that already ended
// (a Wi-Fi roam, a USB hiccup) being drained in one burst: a lower bitrate
// would not have prevented it, and restarting the encoder for it would
// only add a second freeze.
func decodeQueueCongested(stats *clientStatsMsg) bool {
	return stats.QueuedFrames >= congestQueueDepth && stats.StaleFrames == 0
}

// takeStaleFrames returns the client's stale-frame skips since the last call.
func (m *sessionMonitor) takeStaleFrames() int64 {
	return m.staleFrames.Swap(0)
}

// consumeCongestion returns true at most once per congestion episode.
func (m *sessionMonitor) consumeCongestion() bool {
	return m.congested.Swap(false)
}

// consumeLinkStall reports whether a slow socket write contributed to the
// congestion just consumed, which is evidence the link (not the tablet's
// decoder) is the bottleneck.
func (m *sessionMonitor) consumeLinkStall() bool {
	return m.linkStalled.Swap(false)
}

// gone reports whether the client's upstream half has closed.
func (m *sessionMonitor) gone() bool { return m.clientGone.Load() }

// stats returns the most recent client report, or nil before the first one.
func (m *sessionMonitor) stats() *clientStatsMsg { return m.lastStats.Load() }

// monitoredWriter wraps the video connection: every write carries a
// dead-peer deadline, and slow (buffer-full) writes raise the congestion
// signal.
type monitoredWriter struct {
	conn net.Conn
	mon  *sessionMonitor
}

func (w *monitoredWriter) Write(p []byte) (int, error) {
	if err := w.conn.SetWriteDeadline(time.Now().Add(videoWriteTimeout)); err != nil {
		return 0, err
	}
	start := time.Now()
	n, err := w.conn.Write(p)
	w.mon.bytesSent.Add(int64(n))
	if d := time.Since(start); d > slowWriteThreshold && err == nil {
		w.mon.linkStalled.Store(true)
		w.mon.congested.Store(true)
		log.Printf("Video send stalled %v (link congested)", d.Round(time.Millisecond))
	}
	return n, err
}

func publishLatestClientVideoStats(stats *clientStatsMsg) {
	if stats == nil {
		return
	}
	copyStats := *stats
	latestClientVideoStats.Lock()
	latestClientVideoStats.value = &copyStats
	latestClientVideoStats.Unlock()
}

func clearLatestClientVideoStats() {
	latestClientVideoStats.Lock()
	latestClientVideoStats.value = nil
	latestClientVideoStats.Unlock()
}

func currentClientVideoStats() *clientStatsMsg {
	latestClientVideoStats.RLock()
	defer latestClientVideoStats.RUnlock()
	if latestClientVideoStats.value == nil {
		return nil
	}
	copyStats := *latestClientVideoStats.value
	return &copyStats
}
