package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"drawsplay/host/internal/h264"
	"drawsplay/host/internal/wifilan"
)

var (
	errWiFiVideoStream   = errors.New("unsupported video packet stream for Wi-Fi; reconnect video")
	errWiFiVideoUpstream = errors.New("unexpected Wi-Fi video upstream record type, epoch or sequence; reconnect video")
)

// wifiVideoConn is a paired Wi-Fi video socket as the video session sees it:
// a net.Conn for its lifetime and deadlines, and a videoEndpoint whose sink
// turns video packets into Wi-Fi frames and whose feedback source reads the
// tablet's upstream records.
type wifiVideoConn struct {
	net.Conn
	sink     *wifiVideoSink
	feedback *wifiFeedback
	once     sync.Once
}

// newWiFiVideoConn wraps an attached video socket. ready blocks until the
// tablet's decoder is ready (see wifiDecoderGate). With stats the tablet's
// upstream stats and keyframe requests are read; without, the feedback source
// stays silent until the conn closes.
func newWiFiVideoConn(conn net.Conn, epoch uint64, maxAccessUnit int, ready func() error, stats bool) *wifiVideoConn {
	c := &wifiVideoConn{
		Conn:     conn,
		sink:     newWiFiVideoSink(conn, epoch, maxAccessUnit, ready),
		feedback: &wifiFeedback{done: make(chan struct{})},
	}
	if stats {
		c.feedback.start(conn, epoch)
	}
	return c
}

func (c *wifiVideoConn) VideoSink() videoSink     { return c.sink }
func (c *wifiVideoConn) Feedback() feedbackSource { return c.feedback }

// Read is not used: the video session reads through Feedback. It blocks until
// the conn closes, like a peer that never sends.
func (c *wifiVideoConn) Read([]byte) (int, error) {
	<-c.feedback.done
	return 0, io.EOF
}

// Write is not used: video goes through VideoSink, which frames it. A raw
// write would corrupt the frame stream, so it is refused.
func (c *wifiVideoConn) Write([]byte) (int, error) {
	return 0, fmt.Errorf("%w: raw write on a Wi-Fi video socket", errWiFiVideoStream)
}

func (c *wifiVideoConn) Close() error {
	c.once.Do(func() { close(c.feedback.done) })
	return c.Conn.Close()
}

// wifiVideoSink turns the session's video packets into the Wi-Fi format: a
// config frame (SPS, PPS, size, rate) once the parameter sets are known and
// the tablet's decoder is ready, then one AUD-delimited access unit per
// picture. It accepts config and NAL packets only; anything else, or a
// stream the baseline path cannot carry, fails the sink for good. One writer.
type wifiVideoSink struct {
	frames    *wifilan.FrameWriter
	epoch     uint64
	maxPacket int
	ready     func() error

	packetizer wifilan.AUPacketizer
	au         []byte
	fps        int
	configSent bool
	sequence   uint32
	start      time.Time
	err        error
}

func newWiFiVideoSink(out io.Writer, epoch uint64, maxAccessUnit int, ready func() error) *wifiVideoSink {
	return &wifiVideoSink{
		frames: wifilan.NewFrameWriter(out), epoch: epoch,
		maxPacket: maxAccessUnit, ready: ready,
		au:       make([]byte, maxAccessUnit),
		sequence: 3, start: time.Now(),
	}
}

func (s *wifiVideoSink) WritePacket(kind byte, payload []byte) error {
	if s.err != nil {
		return s.err
	}
	if len(payload) > s.maxPacket {
		s.err = fmt.Errorf("%w: packet of %d bytes exceeds %d", errWiFiVideoStream, len(payload), s.maxPacket)
		return s.err
	}
	if err := s.handle(kind, payload); err != nil {
		s.err = err
		return err
	}
	return nil
}

func (s *wifiVideoSink) handle(kind byte, payload []byte) error {
	switch kind {
	case videoPacketConfig:
		var cfg struct {
			FPS int `json:"fps"`
		}
		if err := json.Unmarshal(payload, &cfg); err != nil || cfg.FPS < 1 || cfg.FPS > 120 {
			return fmt.Errorf("%w: invalid config packet", errWiFiVideoStream)
		}
		if s.fps == 0 {
			s.fps = cfg.FPS
		}
		return nil
	case videoPacketNAL:
		n, _, err := s.packetizer.Push(payload, s.au)
		if err != nil {
			return err
		}
		if n == 0 {
			return s.maybeSendConfig()
		}
		if !s.configSent {
			return fmt.Errorf("%w: picture before SPS/PPS and config", errWiFiVideoStream)
		}
		if s.packetizer.ParameterSetsChanged() {
			return fmt.Errorf("%w: parameter sets changed mid-session", errWiFiVideoStream)
		}
		err = s.frames.WriteFrame(wifilan.FrameHeader{
			Type: wifilan.FrameVideoAU, Epoch: s.epoch, Sequence: s.sequence, PayloadLength: uint32(n),
			CaptureMonoUS: uint64(time.Since(s.start).Microseconds()), DurationUS: uint32(1000000 / s.fps),
		}, s.au[:n])
		s.sequence++
		return err
	default:
		return fmt.Errorf("%w: type %d", errWiFiVideoStream, kind)
	}
}

func (s *wifiVideoSink) maybeSendConfig() error {
	if s.configSent || s.fps == 0 {
		return nil
	}
	sets, ok := s.packetizer.ParameterSets()
	if !ok {
		return nil
	}
	width, height, err := h264.SPSDimensions(sets.SPS)
	if err != nil {
		return fmt.Errorf("%w: %w", errWiFiVideoStream, err)
	}
	cfg := make([]byte, 16, 16+len(sets.SPS)+len(sets.PPS))
	binary.BigEndian.PutUint16(cfg[0:], 1)
	binary.BigEndian.PutUint16(cfg[2:], uint16(width))
	binary.BigEndian.PutUint16(cfg[4:], uint16(height))
	binary.BigEndian.PutUint16(cfg[6:], uint16(s.fps))
	binary.BigEndian.PutUint16(cfg[8:], uint16(len(sets.SPS)))
	binary.BigEndian.PutUint16(cfg[10:], uint16(len(sets.PPS)))
	cfg = append(append(cfg, sets.SPS...), sets.PPS...)
	if err := s.frames.WriteFrame(wifilan.FrameHeader{
		Type: wifilan.FrameConfig, Epoch: s.epoch, Sequence: 2, PayloadLength: uint32(len(cfg)),
	}, cfg); err != nil {
		return err
	}
	s.configSent = true
	if s.ready != nil {
		if err := s.ready(); err != nil {
			return err
		}
	}
	s.start = time.Now()
	return nil
}

// wifiFeedback reads the tablet's upstream control records on the video
// socket: stats (subtype 8, JSON) and keyframe requests. The attach record
// already used upstream sequence 1, so records start at sequence 2.
type wifiFeedback struct {
	reader *wifilan.FrameReader
	guard  *wifilan.SequenceGuard
	epoch  uint64
	buffer []byte
	done   chan struct{}
}

func (f *wifiFeedback) start(conn net.Conn, epoch uint64) {
	guard, err := wifilan.NewSequenceGuard(epoch)
	if err != nil {
		return
	}
	_ = guard.Accept(wifilan.FrameHeader{Epoch: epoch, Sequence: 1})
	f.reader, f.guard, f.epoch = wifilan.NewFrameReader(conn), guard, epoch
	f.buffer = make([]byte, wifilan.MaxControlSize)
}

func (f *wifiFeedback) Next() (clientStatsMsg, error) {
	if f.reader == nil {
		<-f.done
		return clientStatsMsg{}, io.EOF
	}
	for {
		header, err := f.reader.ReadHeader()
		if err != nil {
			return clientStatsMsg{}, err
		}
		if header.Type != wifilan.FrameControl || header.Epoch != f.epoch {
			return clientStatsMsg{}, errWiFiVideoUpstream
		}
		if err := f.guard.Accept(header); err != nil {
			return clientStatsMsg{}, err
		}
		raw, err := f.reader.ReadPayload(f.buffer)
		if err != nil {
			return clientStatsMsg{}, err
		}
		if len(raw) == 4 && [4]byte(raw) == wifilan.KeyframeRequest {
			return clientStatsMsg{Type: "keyframe"}, nil
		}
		body, err := wifilan.DecodeVideoStats(raw)
		if err != nil {
			return clientStatsMsg{}, err
		}
		var msg clientStatsMsg
		if err := json.Unmarshal(body, &msg); err == nil {
			return msg, nil
		}
		// Tolerate a record this host does not understand.
	}
}
