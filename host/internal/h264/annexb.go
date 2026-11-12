// Package h264 reads and rewrites H.264 Annex-B byte streams: start codes,
// NAL unit headers, stream splitting, access-unit boundaries, Exp-Golomb bit
// fields and SPS parameters. It is the one place the host parses H.264; the
// video session, the access-unit tracker, temporal-layer dropping, the native
// helper reader and the Wi-Fi packetizer all use it.
package h264

// NAL unit types used by the host (H.264 table 7-1).
const (
	TypeSlice  = 1  // coded slice of a non-IDR picture
	TypeIDR    = 5  // coded slice of an IDR picture
	TypeSEI    = 6  // supplemental enhancement information
	TypeSPS    = 7  // sequence parameter set
	TypePPS    = 8  // picture parameter set
	TypeAUD    = 9  // access unit delimiter
	TypeFiller = 12 // filler data
)

// StartCodeLen returns 4 when b opens with 00 00 00 01, 3 when it opens with
// 00 00 01, and 0 otherwise.
func StartCodeLen(b []byte) int {
	switch {
	case len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 1:
		return 4
	case len(b) >= 3 && b[0] == 0 && b[1] == 0 && b[2] == 1:
		return 3
	}
	return 0
}

// HeaderOffset returns the index of the NAL header byte of unit (a NAL unit
// with its 3- or 4-byte start code), or -1 when unit has no start code or
// ends before the header byte.
func HeaderOffset(unit []byte) int {
	n := StartCodeLen(unit)
	if n == 0 || n >= len(unit) {
		return -1
	}
	return n
}

// NALType returns the nal_unit_type of unit (with start code), or -1.
func NALType(unit []byte) int {
	h := HeaderOffset(unit)
	if h < 0 {
		return -1
	}
	return int(unit[h] & 0x1F)
}

// IsFirstSlice reports whether payload, the bytes after a slice's NAL header,
// starts the slice of a new picture: first_mb_in_slice is 0, whose ue(v) code
// is a single 1 bit at the start of the payload.
func IsFirstSlice(payload []byte) bool {
	return len(payload) > 0 && payload[0]&0x80 != 0
}

// IsDroppableNonRef reports whether unit is a non-reference coded slice (type
// 1 with nal_ref_idc 0), which a decoder can do without.
func IsDroppableNonRef(unit []byte) bool {
	h := HeaderOffset(unit)
	if h < 0 {
		return false
	}
	header := unit[h]
	return header&0x1F == TypeSlice && (header>>5)&0x03 == 0
}

// OpensAccessUnit reports whether a NAL unit of nalType that follows a slice
// of the current picture starts a new access unit (H.264 7.4.1.2.3): SEI, SPS,
// PPS, access unit delimiter, types 14-18, or the first slice of the next
// picture. payload is the unit's bytes after its NAL header.
func OpensAccessUnit(nalType byte, payload []byte) bool {
	switch {
	case nalType >= TypeSEI && nalType <= TypeAUD, nalType >= 14 && nalType <= 18:
		return true
	case nalType == TypeSlice || nalType == TypeIDR:
		return IsFirstSlice(payload)
	}
	return false
}

// Split extracts complete NAL units from buf. Units keep their leading start
// code (3- or 4-byte) so a decoder receives standard Annex-B input. The final,
// possibly incomplete unit is returned as rest: a unit is only complete once
// the next start code (or the end of the stream) is seen.
func Split(buf []byte) (units [][]byte, rest []byte) {
	starts := make([]int, 0, 8)
	i := 0
	for i+2 < len(buf) {
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 1 {
			start := i
			// Fold a preceding zero into a 4-byte start code, but never
			// steal the tail byte of the previous unit's start code.
			if i > 0 && buf[i-1] == 0 && (len(starts) == 0 || starts[len(starts)-1] < i-1) {
				start = i - 1
			}
			starts = append(starts, start)
			i += 3
			continue
		}
		i++
	}
	if len(starts) == 0 {
		return nil, buf
	}
	for j := 0; j+1 < len(starts); j++ {
		units = append(units, buf[starts[j]:starts[j+1]])
	}
	return units, buf[starts[len(starts)-1]:]
}

// FlushTail returns a copy of rest as a final unit when it at least holds a
// start code and a header byte (and is 5 or more bytes long). The video
// session uses it to send a stream's trailing unit when no more bytes arrive.
func FlushTail(rest []byte) ([]byte, bool) {
	if len(rest) < 5 || HeaderOffset(rest) < 0 {
		return nil, false
	}
	return append([]byte(nil), rest...), true
}

// SplitAll splits a complete buffer into NAL units, including the trailing one.
func SplitAll(b []byte) [][]byte {
	units, rest := Split(b)
	if tail, ok := FlushTail(rest); ok {
		units = append(units, tail)
	}
	return units
}

// StartCodeCheck classifies how a buffer opens; see LeadingStartCode.
type StartCodeCheck int

const (
	StartCodePresent    StartCodeCheck = iota // two or more zero bytes, then 0x01
	StartCodeIncomplete                       // only zero bytes so far
	StartCodeAbsent                           // anything else
)

// LeadingStartCode classifies how buf opens: with a start code (two or more
// zero bytes, then 0x01), possibly with one (zero bytes only, so far), or not.
func LeadingStartCode(buf []byte) StartCodeCheck {
	zeros := 0
	for zeros < len(buf) && buf[zeros] == 0 {
		zeros++
	}
	switch {
	case zeros == len(buf):
		return StartCodeIncomplete
	case zeros >= 2 && buf[zeros] == 1:
		return StartCodePresent
	default:
		return StartCodeAbsent
	}
}
