package h264

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// vah264enc (GStreamer 1.28, iHD) 1920x1080 High-profile SPS: VUI present
// with timing info but no video_signal_type.
var vaH264SPS = []byte{
	0, 0, 0, 1, 0x27, 0x64, 0x00, 0x2a, 0xac, 0x24, 0xa8, 0x07, 0x80, 0x22, 0x7e, 0x5f,
	0xfc, 0x00, 0x04, 0x00, 0x04, 0x40, 0x00, 0x00, 0x03, 0x00, 0x40, 0x00, 0x00,
	0x1e, 0x27, 0x68, 0x20, 0x10, 0xb8,
}

type spsColour struct {
	signalled bool
	fullRange uint32
	primaries uint32
	transfer  uint32
	matrix    uint32
}

// parseSPSColour walks a rewritten SPS the same way the rewriter does and
// returns its video_signal_type fields.
func parseSPSColour(t *testing.T, nal []byte) spsColour {
	t.Helper()
	h := bytes.IndexByte(nal, 1)
	r := NewBitReader(Unescape(nal[h+2:]))
	must := func(v uint32, err error) uint32 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	profile := must(r.Bits(8))
	must(r.Bits(16))
	must(r.UE())
	if profile == 100 {
		must(r.UE())
		must(r.UE())
		must(r.UE())
		must(r.Bit())
		if must(r.Bit()) == 1 {
			t.Fatal("scaling matrix not expected in fixture")
		}
	}
	must(r.UE())
	if must(r.UE()) == 0 {
		must(r.UE())
	}
	must(r.UE())
	must(r.Bit())
	must(r.UE())
	must(r.UE())
	if must(r.Bit()) == 0 {
		must(r.Bit())
	}
	must(r.Bit())
	if must(r.Bit()) == 1 {
		for i := 0; i < 4; i++ {
			must(r.UE())
		}
	}
	if must(r.Bit()) == 0 {
		return spsColour{}
	}
	if must(r.Bit()) == 1 {
		if must(r.Bits(8)) == 255 {
			must(r.Bits(32))
		}
	}
	if must(r.Bit()) == 1 {
		must(r.Bit())
	}
	if must(r.Bit()) == 0 {
		return spsColour{}
	}
	must(r.Bits(3))
	c := spsColour{signalled: true, fullRange: must(r.Bit())}
	if must(r.Bit()) == 1 {
		c.primaries = must(r.Bits(8))
		c.transfer = must(r.Bits(8))
		c.matrix = must(r.Bits(8))
	}
	return c
}

func wantBT709Limited(t *testing.T, nal []byte) {
	t.Helper()
	got := parseSPSColour(t, nal)
	want := spsColour{signalled: true, fullRange: 0, primaries: 1, transfer: 1, matrix: 1}
	if got != want {
		t.Fatalf("colour = %+v, want %+v", got, want)
	}
}

func TestWithBT709LimitedVUIAddsSignalTypeToVAEncoderSPS(t *testing.T) {
	if parseSPSColour(t, vaH264SPS).signalled {
		t.Fatal("fixture unexpectedly already signals colour")
	}
	out := WithBT709LimitedVUI(vaH264SPS)
	wantBT709Limited(t, out)
	if !bytes.Equal(out[:5], vaH264SPS[:5]) {
		t.Fatalf("start code/header changed: % x", out[:5])
	}
	if again := WithBT709LimitedVUI(out); !bytes.Equal(again, out) {
		t.Fatal("rewrite is not idempotent")
	}
}

func TestWithBT709LimitedVUIAddsVUIWhenAbsent(t *testing.T) {
	w := &BitWriter{}
	w.Bits(66, 8) // profile_idc: baseline
	w.Bits(0xc0, 8)
	w.Bits(40, 8)
	w.UE(0) // sps id
	w.UE(0) // log2_max_frame_num_minus4
	w.UE(2) // pic_order_cnt_type
	w.UE(1) // max_num_ref_frames
	w.Bits(0, 1)
	w.UE(119) // width in mbs - 1
	w.UE(67)  // height in map units - 1
	w.Bits(1, 1)
	w.Bits(1, 1)
	w.Bits(1, 1) // frame_cropping_flag
	w.UE(0)
	w.UE(0)
	w.UE(0)
	w.UE(4)
	w.Bits(0, 1) // vui_parameters_present_flag
	w.Bits(1, 1) // rbsp_stop_one_bit
	nal := append([]byte{0, 0, 0, 1, 0x67}, Escape(w.Bytes())...)

	wantBT709Limited(t, WithBT709LimitedVUI(nal))
}

func TestWithBT709LimitedVUILeavesOtherUnitsAlone(t *testing.T) {
	for _, nal := range [][]byte{
		{0, 0, 0, 1, 0x28, 0xee, 0x3c, 0xb0},
		{0, 0, 0, 1, 0x27, 0x64},
		{0x27, 0x64, 0x00},
	} {
		if got := WithBT709LimitedVUI(nal); !bytes.Equal(got, nal) {
			t.Fatalf("WithBT709LimitedVUI(% x) = % x, want unchanged", nal, got)
		}
	}
}

func TestEscapeRBSPRoundTrip(t *testing.T) {
	raw := []byte{0, 0, 0, 0, 0, 1, 0, 0, 2, 0, 0, 3, 0, 0, 4}
	esc := Escape(raw)
	if bytes.Contains(esc, []byte{0, 0, 1}) || bytes.Contains(esc, []byte{0, 0, 0}) {
		t.Fatalf("escaped payload still contains a start-code prefix: % x", esc)
	}
	if got := Unescape(esc); !bytes.Equal(got, raw) {
		t.Fatalf("round trip = % x, want % x", got, raw)
	}
}

func TestSPSDimensions(t *testing.T) {
	sps, _ := hex.DecodeString("000000016742c028da0280bfe5c044000003000400000300f23c60ca80")
	w, h, err := SPSDimensions(sps)
	if err != nil || w != 640 || h != 360 {
		t.Fatalf("got %d x %d, %v", w, h, err)
	}
	if _, _, err := SPSDimensions([]byte{0, 0, 0, 1, 0x67, 0x42}); err == nil {
		t.Fatal("truncated SPS accepted")
	}
	if _, _, err := SPSDimensions([]byte{0, 0, 0, 1, 0x68, 0x42, 0xc0, 0x28, 0xda}); err == nil {
		t.Fatal("non-SPS accepted")
	}
}
