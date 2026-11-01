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
	"strings"
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
	// minDropPercent: client frame drops count as congestion only when they
	// reach this share of the frames received in the report; 0 (USB) means
	// any drop does.
	minDropPercent int64
	// excludeStaleDrops: a Wi-Fi tablet's catch-up skips after an airtime
	// stall are not congestion (see dropsCongested).
	excludeStaleDrops bool
	// keyframeWanted: the client discarded pictures and waits for an IDR.
	keyframeWanted atomic.Bool
	// lastKeyframeRestart is owned by the session goroutine.
	lastKeyframeRestart time.Time
	// lastAckFrames is the client's latest cumulative frame ack on this
	// connection; owned by the reader goroutine.
	lastAckFrames int64
}

// Frame acknowledgements (flow control borrowed from SuperDisplay, see
// wiki/topics/superdisplay-windows-pipeline.md). A client advertising
// clientFeatureFrameAck reports {"type":"ack","frames":N} upstream, N being
// the pictures it has finished with (decoded, or dropped before decoding)
// on this connection. Native helpers read the count on stdin ("A <n>") and
// keep at most a few frames unacknowledged, encoding only the newest frame
// when the window opens, so neither the link nor the decoder can build a
// backlog. ffmpeg pipelines ignore it.
//
// The counters are process-wide and monotonic so helper restarts and
// reconnects need no coordination: forwarded counts access units written to
// any client, acked the acknowledgements received, and a new connection
// starts with nothing in flight (acked := forwarded).
var frameAcks struct {
	forwarded atomic.Int64
	acked     atomic.Int64
	signal    chan struct{}
}

func init() { frameAcks.signal = make(chan struct{}, 1) }

const clientFeatureFrameAck = "ack1"

// noteFrameForwarded records one picture written to the client, from any
// pipeline: the client acknowledges every picture it receives.
func noteFrameForwarded() { frameAcks.forwarded.Add(1) }

// nalStartsPicture reports whether an Annex-B NAL unit is the first slice
// of a picture (the client counts pictures the same way): an H.264 slice
// with first_mb_in_slice == 0, or an HEVC slice segment with
// first_slice_segment_in_pic_flag set. Both are the first bit after the NAL
// header (ue(v) zero encodes as a single 1 bit).
func nalStartsPicture(codec string, unit []byte) bool {
	off := annexBNALHeaderOffset(unit)
	if off < 0 {
		return false
	}
	if strings.EqualFold(codec, "hevc") || strings.EqualFold(codec, "h265") {
		if off+2 >= len(unit) {
			return false
		}
		nalType := (unit[off] >> 1) & 0x3F
		return nalType <= 31 && unit[off+2]&0x80 != 0
	}
	if off+1 >= len(unit) {
		return false
	}
	nalType := unit[off] & 0x1F
	return (nalType == 1 || nalType == 5) && unit[off+1]&0x80 != 0
}

// resetFrameAcks marks every frame forwarded so far as settled: frames sent
// to a previous connection will never be acknowledged.
func resetFrameAcks() { frameAcks.acked.Store(frameAcks.forwarded.Load()) }

// addFrameAcks credits newly acknowledged frames and wakes the helper feeder.
func addFrameAcks(n int64) {
	if n <= 0 {
		return
	}
	frameAcks.acked.Add(n)
	select {
	case frameAcks.signal <- struct{}{}:
	default:
	}
}

// helperAckedFrames converts the process-wide counters into the count a
// helper started at forwardedAtStart understands: acknowledgements of its
// own frames only, clamped to what it has actually produced. Frames from
// before it (an earlier pipeline) are acknowledged first, in order.
func helperAckedFrames(forwardedAtStart int64) int64 {
	n := frameAcks.acked.Load() - forwardedAtStart
	if produced := frameAcks.forwarded.Load() - forwardedAtStart; n > produced {
		n = produced
	}
	return max(n, 0)
}

// keyframeRequestCooldown spaces encoder restarts for client keyframe
// requests; a request inside it is held, not dropped.
const keyframeRequestCooldown = time.Second

// wifiMinDropPercent: a paired Wi-Fi tablet drops the odd frame to arrival
// and presentation jitter that a lower bitrate cannot prevent, so only a
// substantial share of dropped frames is evidence of congestion.
const wifiMinDropPercent = 10

func newWiFiSessionMonitor() *sessionMonitor {
	return &sessionMonitor{minDropPercent: wifiMinDropPercent, excludeStaleDrops: true}
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
	resetFrameAcks()
	for scanner.Scan() {
		var stats clientStatsMsg
		if err := json.Unmarshal(scanner.Bytes(), &stats); err != nil {
			continue // tolerate unknown upstream traffic
		}
		if stats.Type == "keyframe" {
			m.keyframeWanted.Store(true)
			continue
		}
		if stats.Type == "ack" {
			if stats.Frames > m.lastAckFrames {
				addFrameAcks(stats.Frames - m.lastAckFrames)
				m.lastAckFrames = stats.Frames
			}
			continue
		}
		if stats.Type != "stats" {
			continue // tolerate unknown upstream traffic
		}
		m.lastStats.Store(&stats)
		publishLatestClientVideoStats(&stats)
		if stats.StaleFrames > 0 {
			m.staleFrames.Add(stats.StaleFrames)
		}
		if decodeQueueCongested(&stats) || m.dropsCongested(&stats) {
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

// dropsCongested reports whether the client dropped enough frames for a lower
// bitrate to help. On Wi-Fi the stale catch-up skips that follow an airtime
// stall are excluded: the stall already ended, and the encoder restart a
// step-down costs would only add a second freeze.
func (m *sessionMonitor) dropsCongested(stats *clientStatsMsg) bool {
	drops := stats.FrameDrops
	if m.excludeStaleDrops {
		drops -= min(stats.StaleFrames, drops)
	}
	if drops <= 0 {
		return false
	}
	return drops*100 >= m.minDropPercent*max(stats.FramesReceived, 1)
}

// consumeKeyframeRequest reports a pending client keyframe request once the
// cooldown since the last honoured one has passed.
func (m *sessionMonitor) consumeKeyframeRequest(now time.Time) bool {
	if !m.keyframeWanted.Load() || now.Sub(m.lastKeyframeRestart) < keyframeRequestCooldown {
		return false
	}
	m.keyframeWanted.Store(false)
	m.lastKeyframeRestart = now
	return true
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
