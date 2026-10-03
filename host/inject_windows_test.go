//go:build windows

package main

import "testing"

func TestPointerFlagsFor(t *testing.T) {
	cases := []struct {
		action       string
		hoverCapable bool
		want         uint32
		ok           bool
	}{
		{"down", true, pointerFlagDown | pointerFlagInRange | pointerFlagInContact, true},
		{"move", true, pointerFlagUpdate | pointerFlagInRange | pointerFlagInContact, true},
		{"up", true, pointerFlagUp | pointerFlagInRange, true},
		{"cancel", true, pointerFlagUp | pointerFlagCanceled, true},
		{"hover_move", true, pointerFlagUpdate | pointerFlagInRange, true},
		{"hover_exit", true, pointerFlagUpdate, true},
		{"hover_move", false, 0, false}, // touch has no hover
		{"other", true, 0, false},
	}
	for _, c := range cases {
		got, ok := pointerFlagsFor(c.action, c.hoverCapable)
		if got != c.want || ok != c.ok {
			t.Errorf("pointerFlagsFor(%q, %v) = (0x%x, %v), want (0x%x, %v)",
				c.action, c.hoverCapable, got, ok, c.want, c.ok)
		}
	}
}

func TestPenPressure(t *testing.T) {
	if got := penPressure(0); got != 1 {
		t.Errorf("penPressure(0) = %d, want 1 (contact implies nonzero)", got)
	}
	if got := penPressure(1); got != penPressureMax {
		t.Errorf("penPressure(1) = %d, want %d", got, penPressureMax)
	}
	if got := penPressure(2); got != penPressureMax {
		t.Errorf("penPressure(2) = %d, want clamped %d", got, penPressureMax)
	}
	if got := penPressure(0.5); got != penPressureMax/2 {
		t.Errorf("penPressure(0.5) = %d, want %d", got, penPressureMax/2)
	}
}

func TestPenTilt(t *testing.T) {
	// Tilt straight "up" (orientation 0) should be pure negative Y tilt.
	tx, ty := penTilt(0.5, 0)
	if tx != 0 || ty >= 0 {
		t.Errorf("penTilt(0.5, 0) = (%d, %d), want (0, <0)", tx, ty)
	}
	// Orientation 90° (pointing right) should be pure positive X tilt.
	tx, ty = penTilt(0.5, 3.1415926/2)
	if tx <= 0 || ty != 0 {
		t.Errorf("penTilt(0.5, pi/2) = (%d, %d), want (>0, 0)", tx, ty)
	}
	// No tilt -> zero on both axes.
	tx, ty = penTilt(0, 1)
	if tx != 0 || ty != 0 {
		t.Errorf("penTilt(0, 1) = (%d, %d), want (0, 0)", tx, ty)
	}
}
