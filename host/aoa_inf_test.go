package main

import (
	"strings"
	"testing"
	"time"
)

func TestAOAWinUSBHardwareIDs(t *testing.T) {
	ids := aoaWinUSBHardwareIDs([][2]uint16{{0x04e8, 0x6860}, {0x18d1, 0x2d01}})
	want := []string{
		`USB\VID_18D1&PID_2D00`,
		`USB\VID_18D1&PID_2D01`, `USB\VID_18D1&PID_2D01&MI_00`,
		`USB\VID_18D1&PID_2D02`, `USB\VID_18D1&PID_2D03`,
		`USB\VID_18D1&PID_2D04`, `USB\VID_18D1&PID_2D04&MI_00`,
		`USB\VID_18D1&PID_2D05`, `USB\VID_18D1&PID_2D05&MI_00`,
		`USB\VID_04E8&PID_6860`,
	}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v\nwant %v", ids, want)
	}
}

func TestAOAWinUSBPackageInf(t *testing.T) {
	inf := aoaWinUSBPackageInf(aoaWinUSBHardwareIDs([][2]uint16{{0x04e8, 0x6860}}), time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC))
	for _, want := range []string{
		"USB_Install, USB\\VID_04E8&PID_6860\r\n",
		"USB_Install, USB\\VID_18D1&PID_2D01&MI_00\r\n",
		"CatalogFile = " + aoaWinUSBCatalogName,
		"DriverVer   = 10/16/2026,1.26.289.1861",
		"DeviceInterfaceGUIDs,0x10000,\"" + aoaDeviceInterfaceGUID + "\"",
		"Needs   = WINUSB.NT",
	} {
		if !strings.Contains(inf, want) {
			t.Errorf("INF missing %q", want)
		}
	}
	if strings.Contains(strings.ReplaceAll(inf, "\r\n", ""), "\n") {
		t.Error("INF must use CRLF line endings")
	}
	if classifyWinUSBInf(inf) != winusbInfDrawSplay {
		t.Error("generated INF must be recognized as DrawSplay-owned")
	}
}
