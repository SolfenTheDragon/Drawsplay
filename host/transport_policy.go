package main

// transportWiFiPaired names sessions authenticated by the pinned mutual-TLS
// pairing. It carries no bearer token: the TLS identities are the credential.
const transportWiFiPaired = "wifi-paired"

func transportDisplayName(transport string) string {
	switch transport {
	case transportWiFiPaired:
		return "Wi-Fi (paired)"
	default:
		return transport
	}
}
