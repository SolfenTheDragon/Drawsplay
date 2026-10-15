//go:build linux && amd64

package main

import "testing"

// Values from <linux/usbdevice_fs.h> on 64-bit Linux.
func TestUsbfsIoctlNumbers(t *testing.T) {
	cases := map[string][2]uintptr{
		"CONTROL":        {usbfsControl, 0xC0185500},
		"BULK":           {usbfsBulk, 0xC0185502},
		"CLAIMINTERFACE": {usbfsClaimInterface, 0x8004550F},
		"RELEASE":        {usbfsRelease, 0x80045510},
		"CLEAR_HALT":     {usbfsClearHalt, 0x80045515},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Errorf("USBDEVFS_%s = %#x, want %#x", name, c[0], c[1])
		}
	}
}
