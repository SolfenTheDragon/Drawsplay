package h264

import (
	"bytes"
	"testing"
)

func TestLeadingStartCode(t *testing.T) {
	cases := []struct {
		in   []byte
		want StartCodeCheck
	}{
		{nil, StartCodeIncomplete},
		{[]byte{0}, StartCodeIncomplete},
		{[]byte{0, 0, 0}, StartCodeIncomplete},
		{[]byte{0, 0, 1}, StartCodePresent},
		{[]byte{0, 0, 0, 1, 0x41}, StartCodePresent},
		{[]byte{0, 0, 0, 0, 1, 0x41}, StartCodePresent},
		{[]byte{0, 1, 0x41}, StartCodeAbsent},
		{[]byte{0, 0, 3, 1}, StartCodeAbsent},
		{[]byte{1, 0x41}, StartCodeAbsent},
		{[]byte{0xA5, 0, 0, 1}, StartCodeAbsent},
	}
	for _, c := range cases {
		if got := LeadingStartCode(c.in); got != c.want {
			t.Errorf("LeadingStartCode(% x) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestOpensAccessUnit(t *testing.T) {
	for _, nalType := range []byte{6, 7, 8, 9, 14, 15, 16, 17, 18} {
		if !OpensAccessUnit(nalType, nil) {
			t.Errorf("type %d after a slice must open an access unit", nalType)
		}
	}
	for _, nalType := range []byte{2, 3, 4, 10, 11, 12, 13, 19, 20} {
		if OpensAccessUnit(nalType, []byte{0x88}) {
			t.Errorf("type %d must not open an access unit", nalType)
		}
	}
	for _, nalType := range []byte{1, 5} {
		if !OpensAccessUnit(nalType, []byte{0x88}) {
			t.Errorf("type %d with first_mb_in_slice 0 must open an access unit", nalType)
		}
		if OpensAccessUnit(nalType, []byte{0x40}) {
			t.Errorf("type %d continuing a picture must not open an access unit", nalType)
		}
		if OpensAccessUnit(nalType, nil) {
			t.Errorf("type %d without slice header must not open an access unit", nalType)
		}
	}
}

func TestH264NALType(t *testing.T) {
	if got := NALType([]byte{0, 0, 0, 1, 0x67, 0x42}); got != 7 {
		t.Fatalf("4-byte start code type = %d, want 7", got)
	}
	if got := NALType([]byte{0, 0, 1, 0x65, 0x88}); got != 5 {
		t.Fatalf("3-byte start code type = %d, want 5", got)
	}
	if got := NALType([]byte{0x65, 0x88}); got != -1 {
		t.Fatalf("non-annexb type = %d, want -1", got)
	}
}

func TestHeaderOffset(t *testing.T) {
	cases := []struct {
		in   []byte
		want int
	}{
		{[]byte{0, 0, 1, 0x41}, 3},
		{[]byte{0, 0, 0, 1, 0x41}, 4},
		{[]byte{0, 0, 1}, -1},
		{[]byte{0, 0, 0, 1}, -1},
		{[]byte{0, 1, 0x41}, -1},
		{nil, -1},
	}
	for _, c := range cases {
		if got := HeaderOffset(c.in); got != c.want {
			t.Errorf("HeaderOffset(% x) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIsDroppableNonRef(t *testing.T) {
	cases := []struct {
		in   []byte
		want bool
	}{
		{[]byte{0, 0, 1, 0x01, 0x88}, true},     // type 1, nal_ref_idc 0
		{[]byte{0, 0, 0, 1, 0x21, 0x88}, false}, // type 1, nal_ref_idc 1
		{[]byte{0, 0, 1, 0x05, 0x88}, false},    // IDR
		{[]byte{0x01, 0x88}, false},             // no start code
	}
	for _, c := range cases {
		if got := IsDroppableNonRef(c.in); got != c.want {
			t.Errorf("IsDroppableNonRef(% x) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSplitAllKeepsTheTrailingUnit(t *testing.T) {
	stream := []byte{0, 0, 0, 1, 0x67, 0x42, 0, 0, 1, 0x68, 0xce, 0, 0, 0, 1, 0x65, 0x88, 0x84}
	units := SplitAll(stream)
	want := [][]byte{{0, 0, 0, 1, 0x67, 0x42}, {0, 0, 1, 0x68, 0xce}, {0, 0, 0, 1, 0x65, 0x88, 0x84}}
	if len(units) != len(want) {
		t.Fatalf("got %d units, want %d", len(units), len(want))
	}
	for i := range want {
		if !bytes.Equal(units[i], want[i]) {
			t.Errorf("unit %d = % x, want % x", i, units[i], want[i])
		}
	}
	if _, ok := FlushTail([]byte{0, 0, 1, 0x65}); ok {
		t.Error("FlushTail accepted a 4-byte tail")
	}
}
