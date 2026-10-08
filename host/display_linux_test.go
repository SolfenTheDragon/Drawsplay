//go:build linux

package main

import "testing"

func TestLinuxSessionBackend(t *testing.T) {
	tests := []struct {
		name           string
		sessionType    string
		display        string
		waylandDisplay string
		want           string
	}{
		{name: "explicit x11", sessionType: "x11", display: ":0", waylandDisplay: "wayland-0", want: "x11"},
		{name: "explicit wayland", sessionType: "wayland", display: ":0", waylandDisplay: "wayland-0", want: "wayland"},
		{name: "infer x11", display: ":0", want: "x11"},
		{name: "infer wayland", waylandDisplay: "wayland-0", want: "wayland"},
		{name: "unknown", want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linuxSessionBackend(tt.sessionType, tt.display, tt.waylandDisplay); got != tt.want {
				t.Fatalf("linuxSessionBackend(%q, %q, %q) = %q, want %q",
					tt.sessionType, tt.display, tt.waylandDisplay, got, tt.want)
			}
		})
	}
}
