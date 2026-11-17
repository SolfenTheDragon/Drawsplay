//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The values the kernel headers give (DRM_IOCTL_*); SETCRTC is also the
// request Xorg made in the vkms_prepare_fb trace (RSI 0xc06864a2).
func TestDRMIoctlNumbers(t *testing.T) {
	for name, c := range map[string]struct{ got, want uintptr }{
		"GEM_CLOSE":          {drmIoctlGemClose, 0x40086409},
		"PRIME_HANDLE_TO_FD": {drmIoctlPrimeHandleToFD, 0xc00c642d},
		"PRIME_FD_TO_HANDLE": {drmIoctlPrimeFDToHandle, 0xc00c642e},
		"MODE_GETRESOURCES":  {drmIoctlGetResources, 0xc04064a0},
		"MODE_SETCRTC":       {drmIoctlSetCrtc, 0xc06864a2},
		"MODE_GETENCODER":    {drmIoctlGetEncoder, 0xc01464a6},
		"MODE_GETCONNECTOR":  {drmIoctlGetConnector, 0xc05064a7},
		"MODE_RMFB":          {drmIoctlRmFB, 0xc00464af},
		"MODE_PAGE_FLIP":     {drmIoctlPageFlip, 0xc01864b0},
		"MODE_CREATE_DUMB":   {drmIoctlCreateDumb, 0xc02064b2},
		"MODE_DESTROY_DUMB":  {drmIoctlDestroyDumb, 0xc00464b4},
		"MODE_ADDFB2":        {drmIoctlAddFB2, 0xc06864b8},
	} {
		if c.got != c.want {
			t.Errorf("DRM_IOCTL_%s = %#x, want %#x", name, c.got, c.want)
		}
	}
}

func TestPickProbeMode(t *testing.T) {
	modes := []drmModeInfo{{Hdisplay: 8192, Vdisplay: 8192}, {Hdisplay: 1920, Vdisplay: 1080}, {Hdisplay: 1024, Vdisplay: 768, Type: drmModeTypePreferred}}
	if m, ok := pickProbeMode(modes); !ok || m.Hdisplay != 1024 {
		t.Fatalf("preferred mode not picked: %+v", m)
	}
	if m, ok := pickProbeMode(modes[:2]); !ok || m.Hdisplay != 1920 {
		t.Fatalf("fallback mode = %+v", m)
	}
	if _, ok := pickProbeMode(modes[:1]); ok {
		t.Fatal("oversized mode accepted")
	}
}

func TestDRMCardDiscovery(t *testing.T) {
	root := t.TempDir()
	sys := filepath.Join(root, "class")
	mk := func(card, device string) {
		dev := filepath.Join(root, "devices", device)
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(sys, card), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dev, filepath.Join(sys, card, "device")); err != nil {
			t.Fatal(err)
		}
	}
	mk("card0", "faux/vkms")
	mk("card0-Virtual-1", "faux/vkms")
	mk("card1", "pci0000:00/0000:00:02.0")
	if node, ok := vkmsCardNode(sys); !ok || node != "/dev/dri/card0" {
		t.Fatalf("vkms node = %q %v", node, ok)
	}
	if gpus := realGPUCards(sys); len(gpus) != 1 || gpus[0] != "/dev/dri/card1" {
		t.Fatalf("GPUs = %v", gpus)
	}
}

// TestLiveVkmsProbeWithoutMaster runs the probe against this machine's real
// devices, only when asked (DRAWSPLAY_LIVE_DRM_PROBE=1) and only while
// another program (the X server) is vkms's DRM master: then the mode set is
// refused for permission before the kernel does anything, so nothing on
// screen changes, yet every step before it (buffer allocation on the GPU,
// export, import into vkms, framebuffer) really runs.
func TestLiveVkmsProbeWithoutMaster(t *testing.T) {
	if os.Getenv("DRAWSPLAY_LIVE_DRM_PROBE") != "1" {
		t.Skip("set DRAWSPLAY_LIVE_DRM_PROBE=1 to run against the real DRM devices")
	}
	// main_linux_test.go points sysfsDRMDir at nothing for every other test.
	prev := sysfsDRMDir
	sysfsDRMDir = "/sys/class/drm"
	t.Cleanup(func() { sysfsDRMDir = prev })
	node, ok := vkmsCardNode("/sys/class/drm")
	if !ok {
		t.Skip("vkms is not loaded")
	}
	err := probeVkmsScanout(node, realGPUCards("/sys/class/drm"))
	t.Logf("probe result: %v", err)
	if !errors.Is(err, errVkmsProbeNotMaster) {
		t.Fatalf("want the not-master refusal (X drives vkms here), got %v", err)
	}
}

// TestLiveHeldVkmsProbe runs the real check on a held vkms device, only when
// asked (DRAWSPLAY_LIVE_DRM_PROBE=held, after the display helper's vkms-load
// held vkms and handed it to this user). The X server cannot see the device,
// so this process is its master and really sets a mode on it.
func TestLiveHeldVkmsProbe(t *testing.T) {
	if os.Getenv("DRAWSPLAY_LIVE_DRM_PROBE") != "held" {
		t.Skip("set DRAWSPLAY_LIVE_DRM_PROBE=held after loading vkms held")
	}
	// main_linux_test.go points sysfsDRMDir at nothing for every other test.
	prev := sysfsDRMDir
	sysfsDRMDir = "/sys/class/drm"
	t.Cleanup(func() { sysfsDRMDir = prev })
	node, ok := vkmsCardNode("/sys/class/drm")
	if !ok {
		t.Fatal("vkms is not loaded")
	}
	if !vkmsHeldFromX(node) {
		t.Fatal("vkms is not held from the X server; refusing to set a mode on it")
	}
	gpus := realGPUCards("/sys/class/drm")
	err := probeVkmsScanout(node, gpus)
	var scanout *vkmsScanoutError
	switch {
	case err == nil:
		t.Logf("vkms shows frames from %v: it would be released to the X server", gpus)
	case errors.As(err, &scanout):
		t.Logf("vkms refused, X never saw it: %v", err)
	default:
		t.Fatalf("the check could not run: %v", err)
	}
}
