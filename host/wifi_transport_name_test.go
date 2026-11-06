package main

import "testing"

func TestPairedWiFiTransportNeedsNoTokenAndIsDistinct(t *testing.T) {
	if transportWiFiPaired == "wifi" || transportWiFiPaired == "usb" || transportWiFiPaired == "" {
		t.Fatalf("name %q collides", transportWiFiPaired)
	}
	if transportDisplayName(transportWiFiPaired) != "Wi-Fi (paired)" {
		t.Fatal(transportDisplayName(transportWiFiPaired))
	}
	var o sessionOwnership
	if !o.acquireInput(transportWiFiPaired) || o.acquireInput("usb") {
		t.Fatal("ownership arbitration")
	}
}
