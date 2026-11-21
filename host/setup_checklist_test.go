package main

import "testing"

func checklistKeys(c setupChecklist) map[string]bool {
	keys := map[string]bool{}
	for _, it := range c.Items {
		keys[it.Key] = true
	}
	return keys
}

func TestSetupChecklistFollowsTransportMode(t *testing.T) {
	def := defaultSettings()
	keys := checklistKeys(currentSetupChecklist(def))
	if !keys["aoa-ready"] || keys["adb-ready"] {
		t.Fatalf("default mode should show only the AOA item, got %v", keys)
	}

	adb := def
	adb.ADBTunnelEnabled = true
	adb.AOAEnabled = false
	keys = checklistKeys(currentSetupChecklist(adb))
	if keys["aoa-ready"] || !keys["adb-ready"] {
		t.Fatalf("adb-only mode should show only the ADB item, got %v", keys)
	}
}

func TestDefaultsPreferAOAOverADB(t *testing.T) {
	d := defaultSettings()
	if !d.AOAEnabled || d.ADBTunnelEnabled {
		t.Fatalf("want AOA on and ADB tunnel off by default, got aoa=%v adb=%v", d.AOAEnabled, d.ADBTunnelEnabled)
	}
}

func TestAOASetupChecklistExplainsMissingWindowsDriver(t *testing.T) {
	item := aoaSetupItem("windows", aoaStatusInfo{
		State:  "error",
		Detail: "WinUSB is not bound to USB device 04e8:6860",
	})
	if item.OK || item.Detail == "" || !containsFold(item.Action, "installer") ||
		!containsFold(item.Action, "accessory mode") {
		t.Fatalf("Windows AOA driver error is not actionable: %+v", item)
	}
}

func TestAOASetupItemOffersSwitchForIdleTablet(t *testing.T) {
	for _, state := range []string{"file-transfer", "manual-switch"} {
		item := aoaSetupItem("linux", aoaStatusInfo{State: state, Detail: "Tablet is in File transfer mode"})
		if item.OK || item.Fix != setupFixRetryUSB || containsFold(item.Detail, "no tablet detected") ||
			!containsFold(item.FixLabel, "accessory mode") {
			t.Fatalf("%s: attached tablet reported wrongly: %+v", state, item)
		}
	}
}

func TestLinuxAOAErrorOffersUdevRuleOnlyForPermissionErrors(t *testing.T) {
	cases := []struct{ detail, want string }{
		{"cannot open 04e8:6860: no permission to open /dev/bus/usb/001/004 (install packaging/linux/70-drawsplay-aoa.rules and replug): permission denied", setupFixUSBRule},
		{"cannot open 04e8:6860: operation not permitted", setupFixUSBRule},
		{"claim accessory interface 0 on /dev/bus/usb/001/005: device or resource busy (is another program using the tablet?)", setupFixRetryUSB},
		{"04e8:6860 did not switch to accessory mode: usbfs control transfer on /dev/bus/usb/001/004: broken pipe", setupFixRetryUSB},
		{"cannot open accessory pipe: read descriptors failed", setupFixRetryUSB},
	}
	for _, c := range cases {
		if item := aoaSetupItem("linux", aoaStatusInfo{State: "error", Detail: c.detail}); item.Fix != c.want || item.Action == "" {
			t.Errorf("%q: got fix %q, want %q", c.detail, item.Fix, c.want)
		}
	}
}
