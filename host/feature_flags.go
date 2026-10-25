package main

import (
	"os"
	"strings"
)

const wifiFeatureFlagEnv = "DRAWSPLAY_FEATURE_WIFI"

// Wi-Fi is available by default (the user toggle stays off); set the
// variable to 0/false/no/off to hide and disable it.
func wifiFeatureEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(wifiFeatureFlagEnv)))
	switch raw {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func applyFeatureFlags(s settings) settings {
	if !wifiFeatureEnabled() {
		s.WifiEnabled = false
		s.PairedWifiEnabled = false
	}
	// A USB transport must stay on: with both off the tablet cannot connect.
	if !s.AOAEnabled && !s.ADBTunnelEnabled {
		s.AOAEnabled = true
	}
	return s
}
