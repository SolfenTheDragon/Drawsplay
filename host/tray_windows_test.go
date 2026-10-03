//go:build windows

package main

import "testing"

func TestTrayConnectionStatusLabel(t *testing.T) {
	if got := trayConnectionStatusLabel(false, "Tablet A"); got != "🔴 Disconnected" {
		t.Fatalf("stopped status = %q, want disconnected", got)
	}
	if got := trayConnectionStatusLabel(true, ""); got != "🔴 Disconnected" {
		t.Fatalf("empty device status = %q, want disconnected", got)
	}
	if got := trayConnectionStatusLabel(true, "unknown-tablet"); got != "🔴 Disconnected" {
		t.Fatalf("unknown device status = %q, want disconnected", got)
	}
	if got := trayConnectionStatusLabel(true, "unknown-client"); got != "🔴 Disconnected" {
		t.Fatalf("unknown client status = %q, want disconnected", got)
	}
	if got := trayConnectionStatusLabel(true, "SM-X920"); got != "🟢 Connected: SM-X920" {
		t.Fatalf("connected status = %q", got)
	}
}
