//go:build linux

package main

import (
	"errors"
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
	var calls atomic.Int32
	prevLoad, prevLoaded, prevAvail, prevPoll := vkmsLoadPrivileged, vkmsIsLoaded, vkmsIsAvailable, vkmsOutputPoll
	vkmsOutputPoll = time.Millisecond
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
		vkmsLoadPrivileged, vkmsIsLoaded, vkmsIsAvailable, vkmsOutputPoll = prevLoad, prevLoaded, prevAvail, prevPoll
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
