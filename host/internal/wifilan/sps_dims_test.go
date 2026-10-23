package wifilan

import (
	"encoding/hex"
	"testing"
)

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
