package main

// EDIDs for a tablet shown through a real display connector (on Linux, a
// spare HDMI port forced on with the kernel's drm.edid_firmware override; see
// kwin_connector_linux.go). The desktop then treats the tablet as an ordinary
// monitor: its display settings list the tablet's native resolution as the
// preferred mode and the standard modes below it, and remember the user's
// choice per monitor.
//
// The EDID is version 1.4 with one CTA-861 extension block:
//
//   - the preferred timing is the tablet's native size at 60 Hz (fewer when
//     60 Hz would need more than an HDMI 1.4 link carries), with CVT
//     reduced blanking;
//   - a range-limits descriptor up to that pixel clock lets the kernel add
//     the standard (DMT) modes that fit, so no mode is larger than the
//     tablet;
//   - the extension's HDMI vendor block makes the connector an HDMI sink:
//     without one the kernel treats it as single-link DVI and drops every
//     mode above 165 MHz (2560x1600 at 60 Hz, for one);
//   - the physical size is left undefined, so the desktop starts at 100%
//     scale, as it does for the host's other virtual displays.
//
// The manufacturer is "DSP" and the serial number encodes the native size, so
// each tablet size is a monitor of its own to the desktop, and the host can
// tell its own EDID from a real monitor's (isDrawsplayEDID).

import (
	"encoding/binary"
	"fmt"
	"math"
)

const (
	edidBlockSize = 128
	// edidManufacturer is "DSP" in the EDID's 5-bit letter code.
	edidManufacturer = uint16(('D'-'@')<<10 | ('S'-'@')<<5 | ('P' - '@'))
	edidProductCode  = 0x7464 // "dt"
	// edidMaxTMDSKHz is the most an HDMI 1.4 sink takes; above it the kernel
	// would need HDMI 2.0 scrambling, which no sink is there to answer.
	edidMaxTMDSKHz = 340000
)

// edidTiming is one display timing, sizes in pixels and lines.
type edidTiming struct {
	ClockKHz               int
	HActive, HFront, HSync int
	HBlank                 int
	VActive, VFront, VSync int
	VBlank                 int
	RefreshHz              int
}

// edidCVTTiming computes the CVT 1.1 reduced-blanking timing of w x h at hz,
// the timing monitors use for their native mode. Unlike CVT proper, the width
// is not rounded down to a multiple of 8, so the mode is exactly the tablet's
// (cvtReducedBlanking in x11_extend_linux.go does the same at 60 Hz).
func edidCVTTiming(w, h, hz int) edidTiming {
	const (
		hBlank, hSync, hFront = 160, 32, 48
		vFront, minVBack      = 3, 6
		minVBlankMicros       = 460.0
		clockStepKHz          = 250
	)
	vSync := cvtVSyncLines(w, h)
	hPeriodMicros := (1e6/float64(hz) - minVBlankMicros) / float64(h)
	vBlank := int(minVBlankMicros/hPeriodMicros) + 1
	if minimum := vFront + vSync + minVBack; vBlank < minimum {
		vBlank = minimum
	}
	total := float64(w+hBlank) * float64(h+vBlank) * float64(hz) / 1000
	clock := int(total/clockStepKHz) * clockStepKHz
	return edidTiming{
		ClockKHz: clock,
		HActive:  w, HFront: hFront, HSync: hSync, HBlank: hBlank,
		VActive: h, VFront: vFront, VSync: vSync, VBlank: vBlank,
		RefreshHz: hz,
	}
}

// cvtVSyncLines is CVT's vertical sync width, which encodes the aspect ratio.
func cvtVSyncLines(w, h int) int {
	if h <= 0 {
		return 10
	}
	is := func(a, b int) bool { return w*b == h*a }
	switch {
	case is(4, 3):
		return 4
	case is(16, 9):
		return 5
	case is(16, 10):
		return 6
	case is(5, 4), is(15, 9):
		return 7
	}
	return 10
}

// tabletPreferredTiming is the native mode at 60 Hz, or at the highest rate
// an HDMI 1.4 link carries for a very large panel.
func tabletPreferredTiming(w, h int) edidTiming {
	t := edidCVTTiming(w, h, 60)
	for hz := 59; t.ClockKHz > edidMaxTMDSKHz && hz >= 24; hz-- {
		t = edidCVTTiming(w, h, hz)
	}
	return t
}

// tabletEDID builds the EDID for a tablet whose panel is w x h pixels.
func tabletEDID(w, h int) ([]byte, error) {
	if !plausibleClientMode(w, h) {
		return nil, fmt.Errorf("no EDID for an implausible tablet size %dx%d", w, h)
	}
	t := tabletPreferredTiming(w, h)
	if t.ClockKHz > edidMaxTMDSKHz {
		return nil, fmt.Errorf("a %dx%d panel needs more than an HDMI 1.4 link carries even at 24 Hz", w, h)
	}
	if t.HActive > 4095 || t.VActive > 4095 {
		return nil, fmt.Errorf("a %dx%d panel does not fit an EDID detailed timing", w, h)
	}
	b := make([]byte, 2*edidBlockSize)
	base := b[:edidBlockSize]
	copy(base, []byte{0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00})
	binary.BigEndian.PutUint16(base[8:], edidManufacturer)
	binary.LittleEndian.PutUint16(base[10:], edidProductCode)
	binary.LittleEndian.PutUint32(base[12:], uint32(w)<<16|uint32(h))
	base[16], base[17] = 1, 2026-1990 // week, year of manufacture
	base[18], base[19] = 1, 4         // EDID 1.4
	base[20] = 0x80                   // digital input, depth and interface undefined
	base[21], base[22] = 0, 0         // physical size undefined
	base[23] = 120                    // gamma 2.2
	// RGB 4:4:4, sRGB default colour space, preferred timing is native,
	// continuous frequency (the kernel adds the standard modes in range).
	base[24] = 0x07
	copy(base[25:35], []byte{0xee, 0x91, 0xa3, 0x54, 0x4c, 0x99, 0x26, 0x0f, 0x50, 0x54}) // sRGB primaries
	// Standard timings are left unused (0x01 0x01): the range limits bring
	// the standard modes that fit.
	for i := 38; i < 54; i++ {
		base[i] = 0x01
	}
	putDetailedTiming(base[54:72], t)
	putRangeLimits(base[72:90], t)
	putDescriptorText(base[90:108], 0xfc, "DrawSplay")
	putDescriptorText(base[108:126], 0xff, fmt.Sprintf("%dx%d", w, h))
	base[126] = 1 // one extension block
	base[127] = edidChecksum(base[:127])

	ext := b[edidBlockSize:]
	ext[0], ext[1] = 0x02, 0x03 // CTA-861 extension, revision 3
	// HDMI vendor-specific data block: IEEE OUI 00-0C-03, physical address
	// 1.0.0.0, no deep colour, highest TMDS clock in 5 MHz units.
	vsdb := []byte{3<<5 | 7, 0x03, 0x0c, 0x00, 0x10, 0x00, 0x00, byte(edidMaxTMDSKHz / 5000)}
	copy(ext[4:], vsdb)
	ext[2] = byte(4 + len(vsdb)) // no detailed timings follow the data blocks
	ext[3] = 0x00                // no underscan, audio or YCbCr support declared
	ext[127] = edidChecksum(ext[:127])
	return b, nil
}

func putDetailedTiming(d []byte, t edidTiming) {
	binary.LittleEndian.PutUint16(d[0:], uint16(t.ClockKHz/10))
	d[2] = byte(t.HActive)
	d[3] = byte(t.HBlank)
	d[4] = byte(t.HActive>>8)<<4 | byte(t.HBlank>>8)
	d[5] = byte(t.VActive)
	d[6] = byte(t.VBlank)
	d[7] = byte(t.VActive>>8)<<4 | byte(t.VBlank>>8)
	d[8] = byte(t.HFront)
	d[9] = byte(t.HSync)
	d[10] = byte(t.VFront&0xf)<<4 | byte(t.VSync&0xf)
	d[11] = byte(t.HFront>>8)<<6 | byte(t.HSync>>8)<<4 | byte(t.VFront>>4)<<2 | byte(t.VSync>>4)
	// d[12:15]: image size undefined; no border.
	d[17] = 0x1a // digital separate sync, +hsync -vsync (CVT reduced blanking)
}

// putRangeLimits describes the rates the "monitor" takes: up to the
// preferred mode's pixel clock, so the kernel adds no mode larger than it.
func putRangeLimits(d []byte, t edidTiming) {
	hKHz := int(math.Ceil(float64(t.ClockKHz) / float64(t.HActive+t.HBlank)))
	maxHz := 76
	if t.RefreshHz < 50 {
		maxHz = 60
	}
	minHz := 24
	clockTens := (t.ClockKHz + 9999) / 10000
	copy(d, []byte{0, 0, 0, 0xfd, 0})
	d[5], d[6] = byte(minHz), byte(maxHz)
	d[7], d[8] = 15, byte(min(255, hKHz+hKHz/4))
	d[9] = byte(clockTens)
	d[10] = 0x01 // range limits only: no secondary timing formula
	d[11] = 0x0a
	for i := 12; i < 18; i++ {
		d[i] = 0x20
	}
}

func putDescriptorText(d []byte, tag byte, s string) {
	copy(d, []byte{0, 0, 0, tag, 0})
	text := []byte(s)
	if len(text) > 13 {
		text = text[:13]
	}
	n := copy(d[5:], text)
	if n < 13 {
		d[5+n] = '\n'
		for i := 5 + n + 1; i < 18; i++ {
			d[i] = ' '
		}
	}
}

func edidChecksum(b []byte) byte {
	var sum byte
	for _, v := range b {
		sum += v
	}
	return -sum
}

// isDrawsplayEDID reports whether edid is one tabletEDID made: a forced
// connector showing it has no real monitor behind it.
func isDrawsplayEDID(edid []byte) bool {
	return len(edid) >= edidBlockSize &&
		binary.BigEndian.Uint16(edid[8:]) == edidManufacturer &&
		binary.LittleEndian.Uint16(edid[10:]) == edidProductCode
}

// tabletEDIDFirmwareName is the file (under the firmware directory's edid/)
// holding the EDID of a w x h tablet.
func tabletEDIDFirmwareName(w, h int) string {
	return fmt.Sprintf("drawsplay-%dx%d.bin", w, h)
}
