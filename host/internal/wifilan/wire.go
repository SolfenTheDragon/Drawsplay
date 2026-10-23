package wifilan

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	FrameHeaderSize = 36
	MaxControlSize  = 4096
	MaxConfigSize   = 64 * 1024
	MaxAccessUnit   = 1024 * 1024

	FrameHello   byte = 1
	FrameControl byte = 2
	FrameInput   byte = 3
	FrameAttach  byte = 4
	FrameConfig  byte = 5
	FrameVideoAU byte = 6
)

var (
	ErrFrameHeader   = errors.New("invalid Wi-Fi envelope header")
	ErrFrameLength   = errors.New("Wi-Fi payload exceeds record or caller-buffer limit")
	ErrFrameOrder    = errors.New("Wi-Fi header/payload read order violated")
	ErrFrameSequence = errors.New("stale Wi-Fi epoch or non-increasing sequence; establish a fresh session before wrap")
)

type FrameHeader struct {
	Type          byte
	PayloadLength uint32
	Epoch         uint64
	Sequence      uint32
	CaptureMonoUS uint64
	DurationUS    uint32
}

func (h FrameHeader) validate() error {
	limit := uint32(MaxControlSize)
	switch h.Type {
	case FrameHello:
		if h.Epoch != 0 || h.Sequence != 0 {
			return ErrFrameHeader
		}
	case FrameControl, FrameInput, FrameAttach:
	case FrameConfig:
		limit = MaxConfigSize
	case FrameVideoAU:
		limit = MaxAccessUnit
	default:
		return ErrFrameHeader
	}
	if h.Type != FrameHello && (h.Epoch == 0 || h.Sequence == 0) {
		return ErrFrameHeader
	}
	if h.Type != FrameVideoAU && h.DurationUS != 0 {
		return ErrFrameHeader
	}
	if h.PayloadLength == 0 || h.PayloadLength > limit {
		return ErrFrameLength
	}
	return nil
}

// FrameReader has one owner and reuses its header storage across records.
type FrameReader struct {
	input   io.Reader
	header  [FrameHeaderSize]byte
	pending uint32
}

func NewFrameReader(input io.Reader) *FrameReader {
	return &FrameReader{input: input}
}

// ReadFrame borrows destination; returned body remains owned by the caller.
// Authentication and sequence checks are separate from byte decoding.
func (r *FrameReader) ReadFrame(destination []byte) (FrameHeader, []byte, error) {
	header, err := r.ReadHeader()
	if err != nil {
		return FrameHeader{}, nil, err
	}
	body, err := r.ReadPayload(destination)
	return header, body, err
}

// ReadHeader validates lengths before a caller checks out any payload slot.
func (r *FrameReader) ReadHeader() (FrameHeader, error) {
	if r.pending != 0 {
		return FrameHeader{}, ErrFrameOrder
	}
	if _, err := io.ReadFull(r.input, r.header[:]); err != nil {
		return FrameHeader{}, err
	}
	raw := r.header[:]
	if raw[1] != 0 || binary.BigEndian.Uint16(raw[2:4]) != FrameHeaderSize ||
		binary.BigEndian.Uint32(raw[32:36]) != 0 {
		return FrameHeader{}, ErrFrameHeader
	}
	header := FrameHeader{
		Type: raw[0], PayloadLength: binary.BigEndian.Uint32(raw[4:8]),
		Epoch: binary.BigEndian.Uint64(raw[8:16]), Sequence: binary.BigEndian.Uint32(raw[16:20]),
		CaptureMonoUS: binary.BigEndian.Uint64(raw[20:28]), DurationUS: binary.BigEndian.Uint32(raw[28:32]),
	}
	if err := header.validate(); err != nil {
		return FrameHeader{}, err
	}
	r.pending = header.PayloadLength
	return header, nil
}

func (r *FrameReader) ReadPayload(destination []byte) ([]byte, error) {
	if r.pending == 0 {
		return nil, ErrFrameOrder
	}
	if uint64(r.pending) > uint64(len(destination)) {
		return nil, ErrFrameLength
	}
	body := destination[:int(r.pending)]
	if _, err := io.ReadFull(r.input, body); err != nil {
		return nil, err
	}
	r.pending = 0
	return body, nil
}

// FrameWriter has one owner and writes payload storage without copying it.
type FrameWriter struct {
	output io.Writer
	header [FrameHeaderSize]byte
}

func NewFrameWriter(output io.Writer) *FrameWriter {
	return &FrameWriter{output: output}
}

func (w *FrameWriter) WriteFrame(header FrameHeader, payload []byte) error {
	if err := header.validate(); err != nil {
		return err
	}
	if uint64(len(payload)) != uint64(header.PayloadLength) {
		return ErrFrameLength
	}
	raw := w.header[:]
	clear(raw)
	raw[0] = header.Type
	binary.BigEndian.PutUint16(raw[2:4], FrameHeaderSize)
	binary.BigEndian.PutUint32(raw[4:8], header.PayloadLength)
	binary.BigEndian.PutUint64(raw[8:16], header.Epoch)
	binary.BigEndian.PutUint32(raw[16:20], header.Sequence)
	binary.BigEndian.PutUint64(raw[20:28], header.CaptureMonoUS)
	binary.BigEndian.PutUint32(raw[28:32], header.DurationUS)
	if err := writeAll(w.output, raw); err != nil {
		return err
	}
	return writeAll(w.output, payload)
}

func writeAll(output io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := output.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

type SequenceGuard struct {
	epoch uint64
	last  uint32
}

func NewSequenceGuard(epoch uint64) (*SequenceGuard, error) {
	if epoch == 0 {
		return nil, ErrFrameSequence
	}
	return &SequenceGuard{epoch: epoch}, nil
}

func (g *SequenceGuard) Accept(header FrameHeader) error {
	if header.Epoch != g.epoch || header.Sequence == 0 || header.Sequence <= g.last {
		return ErrFrameSequence
	}
	g.last = header.Sequence
	return nil
}
