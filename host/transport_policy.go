package main

import "strings"

// transportWiFiPaired names sessions authenticated by the pinned mutual-TLS
// pairing; it has no bearer token and is separate from the legacy "wifi" path.
const transportWiFiPaired = "wifi-paired"

func transportAuthToken(cfg settings, transport string) (string, bool) {
	switch transport {
	case "wifi":
		return strings.TrimSpace(cfg.WifiAuthToken), true
	default:
		return "", false
	}
}

func transportMaxBitrate(cfg settings, transport string) string {
	switch transport {
	case "wifi":
		return cfg.WifiMaxBitrate
	default:
		return ""
	}
}

func transportDisplayName(transport string) string {
	switch transport {
	case "wifi":
		return "Wi-Fi"
	case transportWiFiPaired:
		return "Wi-Fi (paired)"
	default:
		return transport
	}
}
