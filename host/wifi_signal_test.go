package main

import "testing"

func TestWiFiTabletLinkSampleReachesTheStatusStats(t *testing.T) {
	defer clearLatestClientVideoStats()
	feedWiFiStatsLines(t, `{"type":"stats","intervalMs":500,"framesReceived":30,"framesRendered":30,"avgDecodeMs":0,"queuedFrames":0,"wifiRssiDbm":-61,"wifiLinkMbps":433,"wifiFrequencyMhz":5180}`)
	got := currentClientVideoStats()
	if got == nil || got.WiFiRssiDbm != -61 || got.WiFiLinkMbps != 433 || got.WiFiFrequencyMHz != 5180 {
		t.Fatalf("status stats = %+v, want the tablet's Wi-Fi link sample", got)
	}
}
