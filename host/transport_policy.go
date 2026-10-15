package main

import "strings"

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
	default:
		return transport
	}
}
