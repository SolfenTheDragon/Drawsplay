package main

import (
	"os"
	"strings"
)

const wifiFeatureFlagEnv = "DRAWSPLAY_FEATURE_WIFI"

func wifiFeatureEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(wifiFeatureFlagEnv)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func applyFeatureFlags(s settings) settings {
	if !wifiFeatureEnabled() {
		s.WifiEnabled = false
	}
	return s
}
