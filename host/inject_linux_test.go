//go:build linux

package main

import (
	"math"
	"testing"
)

// xServerPixel is the X screen pixel an absolute axis value lands on with
// no transformation matrix: xf86-input-libinput scales the 0..absRange axis
// to 0..0xffffff, and the X server scales that to the screen.
func xServerPixel(v int32, length int) int {
	libinput := float64(v) * 0xffffff / (absRange + 1)
	return int(math.Floor(libinput * float64(length) / 0x1000000))
}

func TestTabletAbsCoordsSpanTheDeviceByDefault(t *testing.T) {
	extended := displayRect{X: 1920, W: 1920, H: 1080}
	for _, target := range []*captureTarget{
		nil,
		{Kind: captureX11Grab}, // mirror
		{Kind: capturePipeWire, Rect: extended},
		{Kind: captureDDAGrab, Rect: extended, DesktopWidth: 3840, DesktopHeight: 1080},
		{Kind: captureX11Grab, Rect: extended, DesktopWidth: 3840, DesktopHeight: 1080, TabletMapped: true},
		{Kind: captureX11Grab, Rect: extended}, // screen size unknown
	} {
		for _, n := range []float64{-0.5, 0, 0.3, 0.5, 0.999, 1, 1.5} {
			x, y := tabletAbsCoords(n, 1-n, target)
			wantX, wantY := int32(clampF(n)*absRange), int32(clampF(1-n)*absRange)
			if x != wantX || y != wantY {
				t.Errorf("tabletAbsCoords(%v, %v, %+v) = %d, %d; want %d, %d", n, 1-n, target, x, y, wantX, wantY)
			}
		}
	}
}

func TestTabletAbsCoordsAimAtTheX11ExtendedDisplay(t *testing.T) {
	for _, target := range []captureTarget{
		{Rect: displayRect{X: 1920, Y: 0, W: 1920, H: 1080}, DesktopWidth: 3840, DesktopHeight: 1080},
		{Rect: displayRect{X: 0, Y: 0, W: 2960, H: 1848}, DesktopWidth: 4880, DesktopHeight: 1848},
		{Rect: displayRect{X: 100, Y: 1080, W: 1280, H: 800}, DesktopWidth: 1920, DesktopHeight: 1880},
		{Rect: displayRect{X: 4480, Y: 200, W: 2960, H: 1848}, DesktopWidth: 7440, DesktopHeight: 2048},
	} {
		target.Kind = captureX11Grab
		r := target.Rect
		for _, k := range []int{0, 1, r.W / 3, r.W / 2, r.W - 2, r.W - 1} {
			x, _ := tabletAbsCoords(float64(k)/float64(r.W-1), 0, &target)
			if got := xServerPixel(x, target.DesktopWidth); got != r.X+k {
				t.Errorf("%+v: column %d lands on screen x %d, want %d", r, k, got, r.X+k)
			}
		}
		for _, k := range []int{0, 1, r.H / 3, r.H / 2, r.H - 2, r.H - 1} {
			_, y := tabletAbsCoords(0, float64(k)/float64(r.H-1), &target)
			if got := xServerPixel(y, target.DesktopHeight); got != r.Y+k {
				t.Errorf("%+v: row %d lands on screen y %d, want %d", r, k, got, r.Y+k)
			}
		}
		// Positions past the tablet's edges stay on the display's edges.
		x, y := tabletAbsCoords(-0.25, 1.25, &target)
		if gx, gy := xServerPixel(x, target.DesktopWidth), xServerPixel(y, target.DesktopHeight); gx != r.X || gy != r.Y+r.H-1 {
			t.Errorf("%+v: out-of-range touch lands on %d,%d", r, gx, gy)
		}
	}
}

func TestX11ScreenAbsStaysInRange(t *testing.T) {
	if got := x11ScreenAbs(3000, 10, 1920, 1); got != absRange {
		t.Errorf("x11ScreenAbs right of the screen = %d, want %d", got, absRange)
	}
	if got := x11ScreenAbs(-10, 20, 1920, 0); got != 0 {
		t.Errorf("x11ScreenAbs left of the screen = %d, want 0", got)
	}
}
