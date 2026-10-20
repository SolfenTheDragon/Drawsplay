package main

import (
	"slices"
	"testing"
)

func TestParseBitrateKbps(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"8M", 8000},
		{"2.5M", 2500},
		{"2500k", 2500},
		{"2500K", 2500},
		{"800000", 800},
		{"", 8000},        // unparseable -> default
		{"garbage", 8000}, // unparseable -> default
		{"-5M", 8000},     // non-positive -> default
	}

	for _, c := range cases {
		if got := parseBitrateKbps(c.in); got != c.want {
			t.Errorf("parseBitrateKbps(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestQSVUsesTwoFrameAsyncDepth(t *testing.T) {
	args := encoderArgs("h264_qsv", 60)
	if i := slices.Index(args, "-async_depth"); i < 0 || i+1 >= len(args) || args[i+1] != "2" {
		t.Fatalf("QSV args = %v, want -async_depth 2", args)
	}
}

func TestEncoderArgsUseTwoSecondGOP(t *testing.T) {
	args := encoderArgs("h264_nvenc", 60)
	if i := slices.Index(args, "-g"); i < 0 || i+1 >= len(args) || args[i+1] != "120" {
		t.Fatalf("NVENC args = %v, want -g 120", args)
	}
}

func TestDefaultSettingsFavorSmoothMotion(t *testing.T) {
	s := defaultSettings()
	if s.FPS != 120 || s.Width != 1920 || s.Height != 1080 {
		t.Fatalf("default mode = %dx%d @ %d, want 1920x1080 @ 120", s.Width, s.Height, s.FPS)
	}
}

func TestIsFingerRelease(t *testing.T) {
	for action, want := range map[string]bool{"up": true, "pointer_up": true, "cancel": true, "down": false, "move": false} {
		if got := isFingerRelease(action); got != want {
			t.Errorf("isFingerRelease(%q) = %t, want %t", action, got, want)
		}
	}
}
