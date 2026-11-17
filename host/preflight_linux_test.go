//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestX11VirtualDisplayWarningFor(t *testing.T) {
	none := xrandrState{Outputs: []xrandrOutput{{Name: "eDP-1", Connected: true, Primary: true}}}
	withVirtual := xrandrState{Outputs: []xrandrOutput{{Name: "eDP-1", Connected: true}, {Name: "Virtual-1-1", Connected: true}}}

	prevBlocked, prevSpare := vkmsBlockedFn, spareHDMIConnectorFn
	t.Cleanup(func() { vkmsBlockedFn, spareHDMIConnectorFn = prevBlocked, prevSpare })
	blocked, spare := false, false
	vkmsBlockedFn = func() bool { return blocked }
	spareHDMIConnectorFn = func() bool { return spare }

	if w := x11VirtualDisplayWarningFor(withVirtual, true, true); w != "" {
		t.Fatalf("virtual output present, got warning %q", w)
	}
	if w := x11VirtualDisplayWarningFor(none, false, true); !strings.Contains(w, "Set up virtual display") {
		t.Fatalf("available but unloaded = %q", w)
	}
	if w := x11VirtualDisplayWarningFor(none, true, true); !strings.Contains(w, "log out") {
		t.Fatalf("loaded without output = %q", w)
	}
	if w := x11VirtualDisplayWarningFor(none, false, false); !strings.Contains(w, "no vkms module") {
		t.Fatalf("unavailable = %q", w)
	}

	// A kernel that failed on vkms: the loaded module is offered for
	// unloading, and once it is gone loading it is never suggested.
	blocked = true
	if w := x11VirtualDisplayWarningFor(none, true, true); !strings.Contains(w, "Unload vkms") {
		t.Fatalf("blocked and loaded = %q", w)
	}
	if w := x11VirtualDisplayWarningFor(none, false, true); strings.Contains(w, "Set up virtual display") || !strings.Contains(w, "mirrors") {
		t.Fatalf("blocked, unloaded, no spare connector = %q", w)
	}
	spare = true
	if w := x11VirtualDisplayWarningFor(none, false, true); w != "" {
		t.Fatalf("blocked, unloaded, spare connector free = %q", w)
	}
}

func TestInstalledHelperUsedOnlyWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "current")
	old := filepath.Join(dir, "old")
	if err := os.WriteFile(current, []byte(embeddedDisplayHelper), 0o755); err != nil {
		t.Fatal(err)
	}
	// An older helper knows vkms-load by name but loads vkms unheld.
	if err := os.WriteFile(old, []byte("#!/bin/bash\ncase \"$1\" in\n  vkms-load)\n    modprobe vkms\n    ;;\nesac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !helperCurrentFn(current) {
		t.Fatal("the current helper was not recognised")
	}
	if helperCurrentFn(old) {
		t.Fatal("an older helper would be trusted to load vkms")
	}
}
