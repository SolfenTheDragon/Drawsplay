package main

import (
	"encoding/binary"
	"testing"
)

func TestTabletEDIDIsValid(t *testing.T) {
	for _, size := range [][2]int{{1920, 1200}, {2560, 1600}, {2800, 1752}, {1200, 1920}, {1340, 800}, {2960, 1848}} {
		w, h := size[0], size[1]
		e, err := tabletEDID(w, h)
		if err != nil {
			t.Fatalf("%dx%d: %v", w, h, err)
		}
		if len(e) != 256 {
			t.Fatalf("%dx%d: %d bytes", w, h, len(e))
		}
		for blk := 0; blk < 2; blk++ {
			var sum byte
			for _, v := range e[blk*128 : blk*128+128] {
				sum += v
			}
			if sum != 0 {
				t.Errorf("%dx%d: block %d checksum is off by %d", w, h, blk, sum)
			}
		}
		if string(e[:8]) != "\x00\xff\xff\xff\xff\xff\xff\x00" {
			t.Errorf("%dx%d: bad header", w, h)
		}
		if !isDrawsplayEDID(e) {
			t.Errorf("%dx%d: not recognised as DrawSplay's own", w, h)
		}
		// The preferred mode is exactly the native size.
		d := e[54:72]
		hActive := int(d[2]) | int(d[4]>>4)<<8
		vActive := int(d[5]) | int(d[7]>>4)<<8
		if hActive != w || vActive != h {
			t.Errorf("%dx%d: preferred timing is %dx%d", w, h, hActive, vActive)
		}
		if clk := int(binary.LittleEndian.Uint16(d)) * 10; clk > edidMaxTMDSKHz {
			t.Errorf("%dx%d: preferred clock %d kHz is above HDMI 1.4", w, h, clk)
		}
		// The range limits must admit the preferred mode.
		r := e[72:90]
		if r[3] != 0xfd || int(r[9])*10000 < int(binary.LittleEndian.Uint16(d))*10 {
			t.Errorf("%dx%d: range limits exclude the preferred mode", w, h)
		}
	}
}

func TestTabletPreferredTimingMatchesKnownCVT(t *testing.T) {
	// 1920x1200 at 60 Hz with reduced blanking is a DMT mode: 154 MHz,
	// 2080x1235 total.
	tm := tabletPreferredTiming(1920, 1200)
	if tm.ClockKHz != 154000 || tm.HActive+tm.HBlank != 2080 || tm.VActive+tm.VBlank != 1235 {
		t.Fatalf("got %+v", tm)
	}
}

func TestTabletPreferredTimingLowersRefreshForHugePanels(t *testing.T) {
	tm := tabletPreferredTiming(3840, 2400)
	if tm.ClockKHz > edidMaxTMDSKHz || tm.RefreshHz >= 60 {
		t.Fatalf("got %+v", tm)
	}
}

func TestTabletEDIDRefusesImplausibleSizes(t *testing.T) {
	if _, err := tabletEDID(100, 100); err == nil {
		t.Fatal("accepted 100x100")
	}
}

func TestIsDrawsplayEDIDRejectsOthers(t *testing.T) {
	other := make([]byte, 128)
	copy(other, "\x00\xff\xff\xff\xff\xff\xff\x00\x10\xac") // DEL
	if isDrawsplayEDID(other) || isDrawsplayEDID(nil) {
		t.Fatal("a foreign EDID passed as DrawSplay's own")
	}
}
