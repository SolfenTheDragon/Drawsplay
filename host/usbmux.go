package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	usbMuxMagic      uint16 = 0x4453 // "DS"
	usbMuxVersion    byte   = 1
	usbMuxHeaderSize        = 13
	usbMuxMaxPayload        = 8 * 1024 * 1024
)

const (
	muxStreamControl byte = 1
	muxStreamInput   byte = 2
	muxStreamVideo   byte = 3
	muxStreamStats   byte = 4
)

// muxFrame is the fixed header + payload record described in
// UPGRADE_PLAN_CUSTOM_USB_TRANSPORT.md section 4.2.
type muxFrame struct {
	StreamID byte
	Flags    byte
	Seq      uint32
	Payload  []byte
}

func writeMuxFrame(w io.Writer, frame muxFrame) error {
	if len(frame.Payload) > usbMuxMaxPayload {
		return fmt.Errorf("mux payload too large: %d", len(frame.Payload))
	}
	header := make([]byte, usbMuxHeaderSize)
	binary.BigEndian.PutUint16(header[0:2], usbMuxMagic)
	header[2] = usbMuxVersion
	header[3] = frame.StreamID
	header[4] = frame.Flags
	binary.BigEndian.PutUint32(header[5:9], frame.Seq)
	binary.BigEndian.PutUint32(header[9:13], uint32(len(frame.Payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(frame.Payload) == 0 {
		return nil
	}
	_, err := w.Write(frame.Payload)
	return err
}

func readMuxFrame(r io.Reader) (muxFrame, error) {
	var header [usbMuxHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return muxFrame{}, err
	}
	if got := binary.BigEndian.Uint16(header[0:2]); got != usbMuxMagic {
		return muxFrame{}, fmt.Errorf("invalid mux magic 0x%04x", got)
	}
	if header[2] != usbMuxVersion {
		return muxFrame{}, fmt.Errorf("unsupported mux version %d", header[2])
	}
	size := binary.BigEndian.Uint32(header[9:13])
	if size > usbMuxMaxPayload {
		return muxFrame{}, fmt.Errorf("mux payload too large: %d", size)
	}
	payload := make([]byte, int(size))
	if size > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return muxFrame{}, err
		}
	}
	return muxFrame{
		StreamID: header[3],
		Flags:    header[4],
		Seq:      binary.BigEndian.Uint32(header[5:9]),
		Payload:  payload,
	}, nil
}
