//go:build linux

package main

import (
	"reflect"
	"testing"
)

func withFakeX11RootSize(t *testing.T, width, height int, ok bool) *[]string {
	t.Helper()
	var displays []string
	prev := x11RootSizeProbe
	x11RootSizeProbe = func(display string) (int, int, bool) {
		displays = append(displays, display)
		return width, height, ok
	}
	t.Cleanup(func() { x11RootSizeProbe = prev })
	return &displays
}

func TestParseXdpyinfoDimensions(t *testing.T) {
	single := `name of display:    :0
version number:    11.0
default screen number:    0
number of screens:    1

screen #0:
  dimensions:    1920x1080 pixels (508x285 millimeters)
  resolution:    96x96 dots per inch
`
	if w, h, ok := parseXdpyinfoDimensions(single); !ok || w != 1920 || h != 1080 {
		t.Errorf("single screen = %dx%d %v, want 1920x1080", w, h, ok)
	}
	multi := `default screen number:    1
number of screens:    2

screen #0:
  dimensions:    1280x1024 pixels (338x270 millimeters)

screen #1:
  dimensions:    3840x2160 pixels (600x340 millimeters)
`
	if w, h, ok := parseXdpyinfoDimensions(multi); !ok || w != 3840 || h != 2160 {
		t.Errorf("default screen 1 = %dx%d %v, want 3840x2160", w, h, ok)
	}
	if _, _, ok := parseXdpyinfoDimensions("xdpyinfo:  unable to open display \":9\".\n"); ok {
		t.Error("parsed a size from an error message")
	}
}

func TestParseXrandrCurrentSize(t *testing.T) {
	out := "Screen 0: minimum 320 x 200, current 2560 x 1440, maximum 16384 x 16384\n" +
		"eDP-1 connected primary 2560x1440+0+0 (normal left inverted right x axis y axis) 309mm x 174mm\n"
	if w, h, ok := parseXrandrCurrentSize(out); !ok || w != 2560 || h != 1440 {
		t.Errorf("xrandr = %dx%d %v, want 2560x1440", w, h, ok)
	}
	if _, _, ok := parseXrandrCurrentSize("Can't open display :9\n"); ok {
		t.Error("parsed a size from an error message")
	}
}

func TestX11CaptureSizePrefersTheTargetSize(t *testing.T) {
	displays := withFakeX11RootSize(t, 3840, 2160, true)
	if w, h := x11CaptureSize(captureTarget{Kind: captureX11Grab, Width: 1280, Height: 800}, ":0"); w != 1280 || h != 800 {
		t.Errorf("sized target = %dx%d, want 1280x800", w, h)
	}
	if len(*displays) != 0 {
		t.Errorf("probed the X server for a sized target: %v", *displays)
	}
	if w, h := x11CaptureSize(captureTarget{Kind: captureX11Grab}, ":1"); w != 3840 || h != 2160 {
		t.Errorf("mirror target = %dx%d, want the X screen's 3840x2160", w, h)
	}
	if !reflect.DeepEqual(*displays, []string{":1"}) {
		t.Errorf("probed displays %v, want [:1]", *displays)
	}
	withFakeX11RootSize(t, 0, 0, false)
	if w, h := x11CaptureSize(captureTarget{Kind: captureX11Grab}, ":1"); w != 0 || h != 0 {
		t.Errorf("unknown screen = %dx%d, want 0x0", w, h)
	}
}
