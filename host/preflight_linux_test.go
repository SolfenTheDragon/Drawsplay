//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestX11VirtualDisplayWarningFor(t *testing.T) {
	none := xrandrState{Outputs: []xrandrOutput{{Name: "eDP-1", Connected: true, Primary: true}}}
	withVirtual := xrandrState{Outputs: []xrandrOutput{{Name: "eDP-1", Connected: true}, {Name: "Virtual-1-1", Connected: true}}}

	if w := x11VirtualDisplayWarningFor(withVirtual, true, true); w != "" {
		t.Fatalf("virtual output present, got warning %q", w)
	}
	if w := x11VirtualDisplayWarningFor(none, false, true); !strings.Contains(w, "modprobe vkms") {
		t.Fatalf("available but unloaded = %q", w)
	}
	if w := x11VirtualDisplayWarningFor(none, true, true); !strings.Contains(w, "log out") {
		t.Fatalf("loaded without output = %q", w)
	}
	if w := x11VirtualDisplayWarningFor(none, false, false); !strings.Contains(w, "no vkms module") {
		t.Fatalf("unavailable = %q", w)
	}
}
