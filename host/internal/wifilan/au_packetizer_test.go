package wifilan

import (
	"bytes"
	"errors"
	"testing"
)

var (
	testSPS = []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x1f, 0xda}
	testPPS = []byte{0, 0, 0, 1, 0x68, 0xce, 0x0f, 0xc8}
	// first_mb_in_slice=0 (bit 1), then arbitrary slice bits.
	testIDR = []byte{0, 0, 1, 0x65, 0xb8, 0x60}
	testP   = []byte{0, 0, 0, 1, 0x41, 0x9a, 0x18}
)

func TestAUPacketizerBuildsAudDelimitedAccessUnitsAndConfig(t *testing.T) {
	var p AUPacketizer
	dst := make([]byte, 64)
	for _, nal := range [][]byte{{0, 0, 0, 1, 0x09, 0x10}, {0, 0, 0, 1, 0x06, 0x05, 0x01}, testSPS, testPPS} {
		n, _, err := p.Push(nal, dst)
		if err != nil || n != 0 {
			t.Fatalf("non-picture NAL produced output n=%d err=%v", n, err)
		}
	}
	config, ok := p.ParameterSets()
	if !ok || !bytes.Equal(config.SPS, testSPS) || !bytes.Equal(config.PPS, testPPS) {
		t.Fatalf("parameter sets not captured: %v", config)
	}
	n, idr, err := p.Push(testIDR, dst)
	if err != nil || !idr || !bytes.Equal(dst[:n], []byte{0, 0, 0, 1, 0x09, 0x10, 0, 0, 0, 1, 0x65, 0xb8, 0x60}) {
		t.Fatalf("IDR AU = % x idr=%v err=%v", dst[:n], idr, err)
	}
	n, idr, err = p.Push(testP, dst)
	if err != nil || idr || !bytes.Equal(dst[:n], []byte{0, 0, 0, 1, 0x09, 0x30, 0, 0, 0, 1, 0x41, 0x9a, 0x18}) {
		t.Fatalf("P AU = % x idr=%v err=%v", dst[:n], idr, err)
	}
}

func TestAUPacketizerRejectsUnsupportedStreams(t *testing.T) {
	dst := make([]byte, 64)
	cases := map[string][]byte{
		"multi-slice picture": {0, 0, 0, 1, 0x41, 0x40}, // first_mb_in_slice != 0.
		"no start code":       {0x41, 0x9a, 0x18},
		"forbidden bit":       {0, 0, 0, 1, 0xe5, 0xb8},
		"empty NAL":           {0, 0, 0, 1},
		"data partition":      {0, 0, 0, 1, 0x42, 0x9a},
	}
	for name, nal := range cases {
		var p AUPacketizer
		if _, _, err := p.Push(nal, dst); !errors.Is(err, ErrUnsupportedAU) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	var p AUPacketizer
	if _, _, err := p.Push(testIDR, dst[:8]); !errors.Is(err, ErrUnsupportedAU) {
		t.Errorf("small destination: err = %v", err)
	}
}

func TestAUPacketizerReportsParameterSetChanges(t *testing.T) {
	var p AUPacketizer
	dst := make([]byte, 64)
	p.Push(testSPS, dst)
	p.Push(testPPS, dst)
	if p.ParameterSetsChanged() {
		t.Fatal("first parameter sets are not a change")
	}
	changed := append(append([]byte{}, testPPS[:len(testPPS)-1]...), 0xc9)
	p.Push(changed, dst)
	if !p.ParameterSetsChanged() {
		t.Fatal("different PPS must require a new decoder configuration")
	}
}
