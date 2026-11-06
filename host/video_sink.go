package main

// The video session talks to the tablet through two small interfaces, so it
// holds no knowledge of the transport:
//
//   - videoSink receives each video packet (config, NAL unit, cursor, tile,
//     frame metadata; see protocol.go).
//   - feedbackSource yields each message the tablet sends back (stats, frame
//     acks, keyframe requests).
//
// USB and ADB carry both as a byte stream on the video connection (packets
// down, JSON lines up): streamVideoSink and lineFeedback. A connection that
// frames them its own way (paired Wi-Fi) implements videoEndpoint and supplies
// its own sink and source (wifi_video_sink.go).

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net"
	"time"
)

type videoSink interface {
	WritePacket(kind byte, payload []byte) error
}

type feedbackSource interface {
	// Next returns the next tablet message. Messages it cannot parse are
	// skipped; an error means the tablet is gone.
	Next() (clientStatsMsg, error)
}

// videoEndpoint is implemented by video connections that bring their own
// framing. Other connections use the byte-stream sink and source.
type videoEndpoint interface {
	VideoSink() videoSink
	Feedback() feedbackSource
}

// videoEndpointsFor returns the sink and feedback source for a video connection.
func videoEndpointsFor(conn net.Conn) (videoSink, feedbackSource) {
	if e, ok := conn.(videoEndpoint); ok {
		return e.VideoSink(), e.Feedback()
	}
	return streamVideoSink{conn}, newLineFeedback(conn)
}

// videoPacketHeaderSize is the [type][length] header of a stream packet.
const videoPacketHeaderSize = 5

// streamVideoSink writes packets as [1-byte type][4-byte big-endian length]
// [payload], the USB and ADB video channel format.
type streamVideoSink struct{ w io.Writer }

func (s streamVideoSink) WritePacket(kind byte, payload []byte) error {
	var header [videoPacketHeaderSize]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if len(payload) == 0 {
		_, err := s.w.Write(header[:])
		return err
	}
	parts := net.Buffers{header[:], payload}
	_, err := parts.WriteTo(s.w)
	return err
}

// lineFeedback reads newline-delimited JSON messages from a byte stream.
type lineFeedback struct{ scanner *bufio.Scanner }

func newLineFeedback(r io.Reader) *lineFeedback {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 512), maxStatsLineBytes)
	return &lineFeedback{scanner}
}

func (f *lineFeedback) Next() (clientStatsMsg, error) {
	for f.scanner.Scan() {
		var msg clientStatsMsg
		if err := json.Unmarshal(f.scanner.Bytes(), &msg); err == nil {
			return msg, nil
		}
		// Tolerate unknown upstream traffic.
	}
	if err := f.scanner.Err(); err != nil {
		return clientStatsMsg{}, err
	}
	return clientStatsMsg{}, io.EOF
}

// monitoredSink times every packet write on the session's connection: a
// write that blocks long means the link is congested. conn carries the write
// deadline that bounds a dead link.
type monitoredSink struct {
	sink videoSink
	conn net.Conn
	mon  *sessionMonitor
}

func (m monitoredSink) WritePacket(kind byte, payload []byte) error {
	if err := m.conn.SetWriteDeadline(time.Now().Add(videoWriteTimeout)); err != nil {
		return err
	}
	start := time.Now()
	err := m.sink.WritePacket(kind, payload)
	if err == nil {
		m.mon.bytesSent.Add(int64(videoPacketHeaderSize + len(payload)))
	}
	if d := time.Since(start); d > slowWriteThreshold && err == nil {
		m.mon.linkStalled.Store(true)
		m.mon.congested.Store(true)
		log.Printf("Video send stalled %v (link congested)", d.Round(time.Millisecond))
	}
	return err
}
