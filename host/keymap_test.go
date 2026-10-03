package main

import "testing"

// Spot-checks of the Android → host key table plus structural invariants.
func TestLookupKeySpotChecks(t *testing.T) {
	cases := []struct {
		name    string
		android int
		vk      uint16
		evdev   uint16
	}{
		{"A", 29, 0x41, 30},
		{"Z", 54, 0x5A, 44},
		{"digit 1", 8, 0x31, 2},
		{"digit 0", 7, 0x30, 11},
		{"enter", 66, 0x0D, 28},
		{"backspace", 67, 0x08, 14},
		{"escape", 111, 0x1B, 1},
		{"left shift", 59, 0xA0, 42},
		{"left ctrl", 113, 0xA2, 29},
		{"left meta", 117, 0x5B, 125},
		{"F1", 131, 0x70, 59},
		{"F12", 142, 0x7B, 88},
		{"arrow up", 19, 0x26, 103},
		{"page down", 93, 0x22, 109},
		{"numpad 5", 149, 0x65, 76},
		{"numpad enter", 160, 0x0D, 96},
	}
	for _, c := range cases {
		m, ok := lookupKey(c.android)
		if !ok {
			t.Errorf("%s (keycode %d): not mapped", c.name, c.android)
			continue
		}
		if m.vk != c.vk || m.evdev != c.evdev {
			t.Errorf("%s (keycode %d): got vk=0x%X evdev=%d, want vk=0x%X evdev=%d",
				c.name, c.android, m.vk, m.evdev, c.vk, c.evdev)
		}
	}
}

func TestLookupKeyUnmapped(t *testing.T) {
	// KEYCODE_VOLUME_UP: deliberately unmapped (device-local function).
	if _, ok := lookupKey(24); ok {
		t.Errorf("keycode 24 should be unmapped")
	}
}

// Every entry must have both host codes; duplicated evdev codes would make
// the uinput keyboard declare a key twice (harmless) but duplicated VK+evdev
// pairs usually indicate a copy-paste error, so flag exact duplicates.
func TestKeymapInvariants(t *testing.T) {
	seen := map[[2]uint16]int{}
	for android, m := range androidKeycodes {
		if m.vk == 0 || m.evdev == 0 {
			t.Errorf("keycode %d: zero host code (vk=0x%X evdev=%d)", android, m.vk, m.evdev)
		}
		key := [2]uint16{m.vk, m.evdev}
		if prev, dup := seen[key]; dup && android != 160 && prev != 160 {
			// numpad enter (160) intentionally shares VK_RETURN with enter.
			t.Errorf("keycodes %d and %d map to identical host codes", prev, android)
		}
		seen[key] = android
	}
}
