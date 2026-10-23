package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"drawsplay/host/internal/wifilan"
)

// wifiVideoBridge lets the existing video session (which writes the USB/legacy
// [type][len][payload] packet stream) feed a paired Wi-Fi video channel
// without modifying that code. It is an io.Writer for one Wi-Fi session only.
type wifiVideoBridge struct {
	out       io.Writer
	frames    *wifilan.FrameWriter
	epoch     uint64
	maxPacket int
	ready     func() error

	header     [5]byte
	headerLen  int
	payload    []byte
	payloadLen int
	want       int

	packetizer wifilan.AUPacketizer
	au         []byte
	fps        int
	configSent bool
	sequence   uint32
	start      time.Time
	err        error
}

func newWiFiVideoBridge(out io.Writer, epoch uint64, maxAccessUnit int, ready func() error) *wifiVideoBridge {
	return &wifiVideoBridge{
		out: out, frames: wifilan.NewFrameWriter(out), epoch: epoch,
		maxPacket: maxAccessUnit, ready: ready,
		payload: make([]byte, maxAccessUnit), au: make([]byte, maxAccessUnit),
		sequence: 3, start: time.Now(),
	}
}

var errWiFiVideoStream = errors.New("unsupported video packet stream for Wi-Fi; reconnect video")

func (b *wifiVideoBridge) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	consumed := 0
	for len(p) > 0 {
		if b.headerLen < len(b.header) {
			n := copy(b.header[b.headerLen:], p)
			b.headerLen += n
			p = p[n:]
			consumed += n
			if b.headerLen < len(b.header) {
				break
			}
			size := binary.BigEndian.Uint32(b.header[1:])
			if size > uint32(b.maxPacket) {
				return consumed, b.fail(fmt.Errorf("%w: packet of %d bytes exceeds %d", errWiFiVideoStream, size, b.maxPacket))
			}
			b.want, b.payloadLen = int(size), 0
		}
		n := copy(b.payload[b.payloadLen:b.want], p)
		b.payloadLen += n
		p = p[n:]
		consumed += n
		if b.payloadLen < b.want {
			break
		}
		kind := b.header[0]
		b.headerLen = 0
		if err := b.handle(kind, b.payload[:b.want]); err != nil {
			return consumed, b.fail(err)
		}
	}
	return consumed, nil
}

func (b *wifiVideoBridge) fail(err error) error {
	b.err = err
	return err
}

func (b *wifiVideoBridge) handle(kind byte, payload []byte) error {
	switch kind {
	case videoPacketConfig:
		var cfg struct {
			FPS int `json:"fps"`
		}
		if err := json.Unmarshal(payload, &cfg); err != nil || cfg.FPS < 1 || cfg.FPS > 120 {
			return fmt.Errorf("%w: invalid config packet", errWiFiVideoStream)
		}
		if b.fps == 0 {
			b.fps = cfg.FPS
		}
		return nil
	case videoPacketNAL:
		n, _, err := b.packetizer.Push(payload, b.au)
		if err != nil {
			return err
		}
		if n == 0 {
			return b.maybeSendConfig()
		}
		if !b.configSent {
			return fmt.Errorf("%w: picture before SPS/PPS and config", errWiFiVideoStream)
		}
		if b.packetizer.ParameterSetsChanged() {
			return fmt.Errorf("%w: parameter sets changed mid-session", errWiFiVideoStream)
		}
		err = b.frames.WriteFrame(wifilan.FrameHeader{
			Type: wifilan.FrameVideoAU, Epoch: b.epoch, Sequence: b.sequence, PayloadLength: uint32(n),
			CaptureMonoUS: uint64(time.Since(b.start).Microseconds()), DurationUS: uint32(1000000 / b.fps),
		}, b.au[:n])
		b.sequence++
		return err
	default:
		return fmt.Errorf("%w: type %d", errWiFiVideoStream, kind)
	}
}

func (b *wifiVideoBridge) maybeSendConfig() error {
	if b.configSent || b.fps == 0 {
		return nil
	}
	sets, ok := b.packetizer.ParameterSets()
	if !ok {
		return nil
	}
	width, height, err := wifilan.SPSDimensions(sets.SPS)
	if err != nil {
		return fmt.Errorf("%w: %v", errWiFiVideoStream, err)
	}
	cfg := make([]byte, 16, 16+len(sets.SPS)+len(sets.PPS))
	binary.BigEndian.PutUint16(cfg[0:], 1)
	binary.BigEndian.PutUint16(cfg[2:], uint16(width))
	binary.BigEndian.PutUint16(cfg[4:], uint16(height))
	binary.BigEndian.PutUint16(cfg[6:], uint16(b.fps))
	binary.BigEndian.PutUint16(cfg[8:], uint16(len(sets.SPS)))
	binary.BigEndian.PutUint16(cfg[10:], uint16(len(sets.PPS)))
	cfg = append(append(cfg, sets.SPS...), sets.PPS...)
	if err := b.frames.WriteFrame(wifilan.FrameHeader{
		Type: wifilan.FrameConfig, Epoch: b.epoch, Sequence: 2, PayloadLength: uint32(len(cfg)),
	}, cfg); err != nil {
		return err
	}
	b.configSent = true
	if b.ready != nil {
		if err := b.ready(); err != nil {
			return err
		}
	}
	b.start = time.Now()
	return nil
}
