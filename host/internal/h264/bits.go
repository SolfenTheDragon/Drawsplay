package h264

import "errors"

var (
	errTruncated  = errors.New("h264: truncated bit field")
	errExpGolomb  = errors.New("h264: bad exp-golomb code")
	errPOCCycle   = errors.New("h264: bad picture order count cycle")
	errDimensions = errors.New("unsupported or truncated SPS; cannot read picture size")
)

// BitReader reads big-endian bit fields from an RBSP (emulation prevention
// already removed; see Unescape).
type BitReader struct {
	data []byte
	pos  int
}

// NewBitReader reads from data, starting at its first bit.
func NewBitReader(data []byte) *BitReader { return &BitReader{data: data} }

// Pos is the index of the next bit to read.
func (r *BitReader) Pos() int { return r.pos }

// Bit reads one bit.
func (r *BitReader) Bit() (uint32, error) {
	if r.pos >= len(r.data)*8 {
		return 0, errTruncated
	}
	b := uint32(r.data[r.pos/8]>>(7-uint(r.pos%8))) & 1
	r.pos++
	return b, nil
}

// Bits reads an n-bit unsigned field (n <= 32).
func (r *BitReader) Bits(n int) (uint32, error) {
	var v uint32
	for i := 0; i < n; i++ {
		b, err := r.Bit()
		if err != nil {
			return 0, err
		}
		v = v<<1 | b
	}
	return v, nil
}

// UE reads an unsigned Exp-Golomb field, ue(v).
func (r *BitReader) UE() (uint32, error) {
	zeros := 0
	for {
		b, err := r.Bit()
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, errExpGolomb
		}
	}
	v, err := r.Bits(zeros)
	return (1<<uint(zeros) - 1) + v, err
}

// SkipUE skips n ue(v) fields.
func (r *BitReader) SkipUE(n int) error {
	for i := 0; i < n; i++ {
		if _, err := r.UE(); err != nil {
			return err
		}
	}
	return nil
}

// BitWriter builds a big-endian bit string.
type BitWriter struct {
	data []byte
	n    int
}

// Bytes returns the bits written so far, the last byte zero-padded.
func (w *BitWriter) Bytes() []byte { return w.data }

// Bits writes the low n bits of v.
func (w *BitWriter) Bits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.data = append(w.data, 0)
		}
		if v>>uint(i)&1 == 1 {
			w.data[len(w.data)-1] |= 1 << (7 - uint(w.n%8))
		}
		w.n++
	}
}

// UE writes v as ue(v).
func (w *BitWriter) UE(v uint32) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	w.Bits(0, n)
	w.Bits(v, n+1)
}

// CopyFrom copies bits [from, to) of r's data.
func (w *BitWriter) CopyFrom(r *BitReader, from, to int) {
	for p := from; p < to; p++ {
		w.Bits(uint32(r.data[p/8]>>(7-uint(p%8)))&1, 1)
	}
}

// Unescape removes emulation-prevention bytes (00 00 03 -> 00 00), turning a
// NAL payload into its RBSP.
func Unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// Escape inserts emulation-prevention bytes so that an RBSP never contains a
// start-code prefix.
func Escape(b []byte) []byte {
	out := make([]byte, 0, len(b)+8)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}
