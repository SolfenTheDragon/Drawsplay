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

func TestWaylandDesktopWarning(t *testing.T) {
	if w := waylandDesktopWarning("X-Cinnamon"); !strings.Contains(w, "Cinnamon on Wayland") || !strings.Contains(w, "Cinnamon (X11) session") {
		t.Fatalf("Cinnamon = %q", w)
	}
	w := waylandDesktopWarning("LXQt")
	if !strings.Contains(w, "This Wayland desktop") || !strings.Contains(w, "X11 session (or GNOME, KDE Plasma, sway or Hyprland)") {
		t.Fatalf("LXQt = %q", w)
	}
	// The setup checklist finds the warning by this word.
	if !strings.Contains(w, "XWayland") {
		t.Fatalf("warning lacks XWayland: %q", w)
	}
}
