package wifilan

import (
	"bytes"
	"errors"
)

// ErrUnsupportedAU reports an encoder stream the Wi-Fi baseline path cannot carry.
var ErrUnsupportedAU = errors.New("unsupported H.264 stream for Wi-Fi: need single-slice progressive baseline NAL units with start codes")

// ParameterSets are 4-byte-start-code SPS and PPS NAL units.
type ParameterSets struct {
	SPS, PPS []byte
}

// AUPacketizer converts a per-NAL encoder stream into the Wi-Fi access-unit
// format: one AUD-delimited picture per AU with no in-band parameter sets.
// It requires one slice per picture so a VCL NAL ends its access unit
// immediately, adding no frame of latency. Single owner; not goroutine-safe.
type AUPacketizer struct {
	sps, pps []byte
	changed  bool
}

var startCode4 = []byte{0, 0, 0, 1}

// Push consumes one NAL unit including its start code. It returns the AU
// length written to dst for a picture NAL, or 0 for NAL units that are
// consumed or dropped (AUD, SEI, filler, SPS, PPS).
func (p *AUPacketizer) Push(nal, dst []byte) (n int, idr bool, err error) {
	prefix := 0
	switch {
	case len(nal) >= 4 && bytes.Equal(nal[:4], startCode4):
		prefix = 4
	case len(nal) >= 3 && nal[0] == 0 && nal[1] == 0 && nal[2] == 1:
		prefix = 3
	}
	if prefix == 0 || len(nal) < prefix+1 || nal[prefix]&0x80 != 0 {
		return 0, false, ErrUnsupportedAU
	}
	header := nal[prefix]
	switch header & 31 {
	case 6, 9, 12:
		return 0, false, nil
	case 7:
		p.sps = p.store(p.sps, nal[prefix:])
		return 0, false, nil
	case 8:
		p.pps = p.store(p.pps, nal[prefix:])
		return 0, false, nil
	case 1, 5:
	default:
		return 0, false, ErrUnsupportedAU
	}
	// first_mb_in_slice is ue(v); zero is the single bit 1.
	if len(nal) < prefix+2 || nal[prefix+1]&0x80 == 0 {
		return 0, false, ErrUnsupportedAU
	}
	idr = header&31 == 5
	body := nal[prefix:]
	if len(dst) < 6+4+len(body) {
		return 0, false, ErrUnsupportedAU
	}
	picType := byte(1)
	if idr {
		picType = 0
	}
	n = copy(dst, startCode4)
	dst[n] = 9
	dst[n+1] = picType<<5 | 0x10
	n += 2
	n += copy(dst[n:], startCode4)
	n += copy(dst[n:], body)
	return n, idr, nil
}

func (p *AUPacketizer) store(current, body []byte) []byte {
	next := make([]byte, 0, 4+len(body))
	next = append(append(next, startCode4...), body...)
	if p.sps != nil && p.pps != nil && current != nil && !bytes.Equal(current, next) {
		p.changed = true
	}
	return next
}

// ParameterSets returns the latest SPS/PPS once both have been seen and
// acknowledges any pending change.
func (p *AUPacketizer) ParameterSets() (ParameterSets, bool) {
	if p.sps == nil || p.pps == nil {
		return ParameterSets{}, false
	}
	p.changed = false
	return ParameterSets{SPS: p.sps, PPS: p.pps}, true
}

// ParameterSetsChanged reports that an SPS/PPS differing from an already
// complete pair arrived since the last ParameterSets call, so the tablet
// decoder needs a fresh configuration (a reconnect in the first implementation).
func (p *AUPacketizer) ParameterSetsChanged() bool { return p.changed }
