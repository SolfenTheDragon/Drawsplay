//go:build linux

package main

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// withVkmsSetupTest fakes an X11 session without a virtual output; load runs
// instead of the privileged vkms load.
func withVkmsSetupTest(t *testing.T, loaded, available bool, load func(f *fakeXrandr) error) (*fakeXrandr, *atomic.Int32) {
	t.Helper()
	f := newFakeLaptopWithVKMS()
	virtual := f.outputs[2]
	f.outputs = f.outputs[:2]
	withFakeX11Ext(t, f)
	t.Setenv("XDG_SESSION_TYPE", "x11")
	t.Setenv(vkmsAutoloadEnv, "1")
	// Never touch the real DRM connectors from tests.
	prevSysfs, prevForce := sysfsDRMDir, forceConnectorPrivileged
	sysfsDRMDir = t.TempDir()
	forceConnectorPrivileged = func(string, string, bool) error { return errors.New("forcing connectors is disabled in tests") }
	t.Cleanup(func() { sysfsDRMDir, forceConnectorPrivileged = prevSysfs, prevForce })
	var calls atomic.Int32
	prevLoad, prevLoaded, prevAvail, prevPoll, prevCheck := vkmsLoadPrivileged, vkmsIsLoaded, vkmsIsAvailable, vkmsOutputPoll, vkmsPostLoadCheck
	vkmsOutputPoll = time.Millisecond
	vkmsPostLoadCheck = func() bool { return false }
	vkmsIsLoaded = func() bool { return loaded }
	vkmsIsAvailable = func() bool { return available }
	vkmsLoadPrivileged = func(bool) error {
		calls.Add(1)
		if err := load(f); err != nil {
			return err
		}
		f.update(func(f *fakeXrandr) { f.outputs = append(f.outputs, virtual) })
		return nil
	}
	vkmsSetup.mu.Lock()
	vkmsSetup.running, vkmsSetup.lastFail = false, time.Time{}
	vkmsSetup.mu.Unlock()
	t.Cleanup(func() {
		vkmsLoadPrivileged, vkmsIsLoaded, vkmsIsAvailable, vkmsOutputPoll, vkmsPostLoadCheck = prevLoad, prevLoaded, prevAvail, prevPoll, prevCheck
		vkmsSetup.mu.Lock()
		vkmsSetup.lastFail = time.Time{}
		vkmsSetup.mu.Unlock()
	})
	return f, &calls
}

func TestEnsureX11VirtualDisplayLoadsVkms(t *testing.T) {
	f, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	ensureX11VirtualDisplay("test", true)
	if calls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1", calls.Load())
	}
	if !x11VirtualDisplayReady() {
		t.Fatal("no virtual output after loading vkms")
	}
	_ = f
}

func TestEnsureX11VirtualDisplaySkipsWhenOutputExists(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	ensureX11VirtualDisplay("first", true)
	ensureX11VirtualDisplay("second", true)
	if calls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1", calls.Load())
	}
}

func TestEnsureX11VirtualDisplayDoesNotLoadWhenNotNeeded(t *testing.T) {
	for name, c := range map[string]struct{ loaded, available bool }{
		"already loaded": {true, true},
		"no module":      {false, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, calls := withVkmsSetupTest(t, c.loaded, c.available, func(*fakeXrandr) error { return nil })
			ensureX11VirtualDisplay("test", true)
			if calls.Load() != 0 {
				t.Fatalf("load calls = %d, want 0", calls.Load())
			}
		})
	}
}

func TestEnsureX11VirtualDisplaySkipsWhenOutputIsChosen(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	t.Setenv(x11OutputEnv, "HDMI-1")
	ensureX11VirtualDisplay("test", true)
	if calls.Load() != 0 {
		t.Fatalf("loaded vkms although %s is set", x11OutputEnv)
	}
}

func TestEnsureX11VirtualDisplayBacksOffAfterAFailedLoad(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return errVkmsPromptDeclined })
	ensureX11VirtualDisplay("first", true)
	ensureX11VirtualDisplay("second", true)
	if calls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1 (cooldown after a declined prompt)", calls.Load())
	}
	vkmsSetup.mu.Lock()
	vkmsSetup.lastFail = time.Now().Add(-2 * vkmsRetryCooldown)
	vkmsSetup.mu.Unlock()
	ensureX11VirtualDisplay("third", true)
	if calls.Load() != 2 {
		t.Fatalf("load calls = %d, want 2 after the cooldown", calls.Load())
	}
}

func TestEnsureX11VirtualDisplayIgnoresNonX11Sessions(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return errors.New("unused") })
	t.Setenv("XDG_SESSION_TYPE", "wayland")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	ensureX11VirtualDisplay("test", true)
	if calls.Load() != 0 {
		t.Fatalf("load calls = %d on a Wayland session", calls.Load())
	}
}

func TestEnsureX11VirtualDisplayCanBeDisabled(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	t.Setenv(vkmsAutoloadEnv, "0")
	ensureX11VirtualDisplay("test", true)
	if calls.Load() != 0 {
		t.Fatalf("vkms was loaded although it is disabled (%d calls)", calls.Load())
	}
}

func TestEnsureX11VirtualDisplayLoadsVkmsByDefault(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	withGuardTest(t)
	t.Setenv(vkmsAutoloadEnv, "")
	ensureX11VirtualDisplay("test", true)
	if calls.Load() != 1 {
		t.Fatalf("vkms is the primary method; load calls = %d", calls.Load())
	}
}

func TestEnsureX11VirtualDisplaySkipsBlockedVkms(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	withGuardTest(t)
	vkmsKernelLogFn = func() bool { return true }
	t.Setenv(vkmsAutoloadEnv, "")
	ensureX11VirtualDisplay("test", true)
	if calls.Load() != 0 {
		t.Fatalf("a blocked vkms was loaded (%d calls)", calls.Load())
	}
}

// The kernel fails on vkms right after the load, while the X server stays
// up: the host must record the denial and move on to the spare connector in
// the same pass, and never load vkms again on this kernel series.
func TestEnsureX11VirtualDisplayFallsBackWhenVkmsFailsInTheKernel(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	withGuardTest(t)
	t.Setenv(vkmsAutoloadEnv, "")
	vkmsPostLoadCheck = func() bool {
		displayGuardDenyUntil(guardMethodVkms, "the kernel log shows a vkms failure (vkms_prepare_fb)", true)
		return true
	}
	fakeSysfsDRM(t, "disconnected", nil)
	forced := 0
	forceConnectorPrivileged = func(path, value string, _ bool) error {
		forced++
		return os.WriteFile(path, []byte("connected\n"), 0o644)
	}
	ensureX11VirtualDisplay("test", true)
	if calls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1", calls.Load())
	}
	if forced == 0 {
		t.Fatal("the spare connector must be tried once vkms failed in the kernel")
	}
	if _, denied := displayGuardDenied(guardMethodVkms); !denied {
		t.Fatal("vkms must be denied after the kernel failure")
	}
	vkmsSetup.mu.Lock()
	vkmsSetup.lastFail = time.Time{}
	vkmsSetup.mu.Unlock()
	ensureX11VirtualDisplay("again", true)
	if calls.Load() != 1 {
		t.Fatalf("vkms was loaded again after the kernel failure (%d calls)", calls.Load())
	}
}
