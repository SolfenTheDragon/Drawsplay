//go:build windows

package main

import "testing"

func TestTabletPanelHz(t *testing.T) {
	clearLatestClientVideoStats()
	defer clearLatestClientVideoStats()
	if got := tabletPanelHz(); got != defaultTabletPanelHz {
		t.Fatalf("no info: got %d, want %d", got, defaultTabletPanelHz)
	}
	publishLatestClientVideoStats(&clientStatsMsg{Type: "stats", RefreshHz: 89.9})
	if got := tabletPanelHz(); got != 90 {
		t.Fatalf("stats refresh: got %d, want 90", got)
	}
	bindSessionClientIdentity(903, clientIdentity{Platform: "android", RefreshRates: []int{60, 144}})
	defer unbindSessionClientIdentity(903)
	if got := tabletPanelHz(); got != 144 {
		t.Fatalf("hello rates: got %d, want 144", got)
	}
}
