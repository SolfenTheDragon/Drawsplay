//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSysfsDRM builds a /sys/class/drm look-alike with one PCI HDMI port.
func fakeSysfsDRM(t *testing.T, status string, edid []byte) (statusFile string) {
	t.Helper()
	root := t.TempDir()
	pci := filepath.Join(root, "devices", "pci0000:00", "0000:00:02.0")
	if err := os.MkdirAll(pci, 0o755); err != nil {
		t.Fatal(err)
	}
	conn := filepath.Join(root, "class", "drm", "card0-HDMI-A-1")
	if err := os.MkdirAll(conn, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pci, filepath.Join(conn, "device")); err != nil {
		t.Fatal(err)
	}
	statusFile = filepath.Join(conn, "status")
	os.WriteFile(statusFile, []byte(status+"\n"), 0o644)
	os.WriteFile(filepath.Join(conn, "edid"), edid, 0o644)
	// A vkms connector must be ignored.
	faux := filepath.Join(root, "devices", "faux", "vkms")
	os.MkdirAll(faux, 0o755)
	vc := filepath.Join(root, "class", "drm", "card1-HDMI-A-1")
	os.MkdirAll(vc, 0o755)
	os.Symlink(faux, filepath.Join(vc, "device"))
	os.WriteFile(filepath.Join(vc, "status"), []byte("disconnected\n"), 0o644)
	prev := sysfsDRMDir
	sysfsDRMDir = filepath.Join(root, "class", "drm")
	// Claims must not leak to or from a host running on the test machine.
	claims := filepath.Join(root, "claims")
	prevClaims := displayClaimsDir
	displayClaimsDir = func() string { return claims }
	t.Cleanup(func() {
		sysfsDRMDir = prev
		displayClaimsDir = prevClaims
	})
	return statusFile
}

func TestForcedOutputRecognition(t *testing.T) {
	fakeSysfsDRM(t, "connected", nil)
	if !isForcedX11Output("HDMI-1") {
		t.Fatal("connected HDMI port without EDID should count as forced")
	}
	if isX11VirtualOutputName("eDP-1") || !isX11VirtualOutputName("Virtual-1-1") || !isX11VirtualOutputName("HDMI-1") {
		t.Fatal("unexpected virtual output classification")
	}
}

func TestRealMonitorIsNotForced(t *testing.T) {
	fakeSysfsDRM(t, "connected", []byte{0, 0xff, 0xff})
	if isForcedX11Output("HDMI-1") {
		t.Fatal("a port with a monitor (EDID) must not be taken as virtual")
	}
}

func TestEnsureForcedConnectorDisplay(t *testing.T) {
	status := fakeSysfsDRM(t, "disconnected", nil)
	f := newFakeLaptopWithVKMS()
	f.outputs = f.outputs[:2] // eDP-1 and a disconnected HDMI-1
	withFakeX11Ext(t, f)
	prev := forceConnectorPrivileged
	t.Cleanup(func() { forceConnectorPrivileged = prev })
	var wrote []string
	forceConnectorPrivileged = func(path, value string, _ bool) error {
		wrote = append(wrote, value)
		if value == "on" {
			os.WriteFile(status, []byte("connected\n"), 0o644)
			f.update(func(f *fakeXrandr) {
				f.outputs[1].connected = true
				f.outputs[1].modes = []string{"1024x768"}
				f.outputs[1].preferred = "1024x768"
			})
		}
		return nil
	}
	if err := ensureForcedConnectorDisplay("test", true); err != nil {
		t.Fatal(err)
	}
	if len(wrote) != 1 || wrote[0] != "on" || !x11VirtualDisplayReady() {
		t.Fatalf("wrote=%v ready=%v", wrote, x11VirtualDisplayReady())
	}
}

func TestEnsureForcedConnectorWithoutSpare(t *testing.T) {
	fakeSysfsDRM(t, "connected", []byte{1})
	if err := ensureForcedConnectorDisplay("test", false); err != errNoSpareConnector {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectorWriteRejectsOddPaths(t *testing.T) {
	if err := writeConnectorStatusPrivileged("/etc/passwd", "on", false); err == nil {
		t.Fatal("accepted a path outside the DRM connector status files")
	}
	if err := writeConnectorStatusPrivileged("/sys/class/drm/card0-HDMI-A-1/status", "off; rm -rf /", false); err == nil {
		t.Fatal("accepted an arbitrary value")
	}
}

func TestReleaseForcedConnectors(t *testing.T) {
	fakeSysfsDRM(t, "connected", nil)
	var wrote []string
	prev := forceConnectorPrivileged
	forceConnectorPrivileged = func(path, value string, mayPrompt bool) error {
		if mayPrompt {
			t.Fatal("release on exit must never prompt")
		}
		wrote = append(wrote, value)
		return nil
	}
	t.Cleanup(func() { forceConnectorPrivileged = prev })
	if n := releaseForcedConnectors(); n != 1 || len(wrote) != 1 || wrote[0] != "detect" {
		t.Fatalf("released %d, wrote %v", n, wrote)
	}
}

func TestReleaseForcedConnectorsLeavesRealMonitors(t *testing.T) {
	fakeSysfsDRM(t, "connected", []byte{0, 0xff, 0xff})
	prev := forceConnectorPrivileged
	forceConnectorPrivileged = func(string, string, bool) error { t.Fatal("must not touch a real monitor"); return nil }
	t.Cleanup(func() { forceConnectorPrivileged = prev })
	if n := releaseForcedConnectors(); n != 0 {
		t.Fatalf("released %d", n)
	}
}
