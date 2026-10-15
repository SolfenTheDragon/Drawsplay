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
