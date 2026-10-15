package main

import (
	"strings"
	"testing"
)

func TestAOAWinUSBInf(t *testing.T) {
	inf := aoaWinUSBInf(0x04e8, 0x6860)
	for _, want := range []string{
		"USB_Install, USB\\VID_04E8&PID_6860",
		"DeviceInterfaceGUIDs,0x10000,\"" + aoaDeviceInterfaceGUID + "\"",
		"Needs   = WINUSB.NT",
	} {
		if !strings.Contains(inf, want) {
			t.Errorf("INF missing %q", want)
		}
	}
}
