//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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
	prevVerify, prevUnload, prevHeld, prevXLog := vkmsVerifyFn, vkmsUnloadHeldFn, vkmsIsHeld, vkmsXLogFailureFn
	vkmsVerifyFn = func(bool) error { return nil }
	vkmsUnloadHeldFn = func(bool) error { return nil }
	vkmsIsHeld = func() bool { return false }
	vkmsXLogFailureFn = func(xorgLogMark) bool { return false }
	prevWatch := vkmsSettleWatch
	vkmsSettleWatch = 0
	t.Cleanup(func() {
		vkmsVerifyFn, vkmsUnloadHeldFn, vkmsIsHeld, vkmsXLogFailureFn = prevVerify, prevUnload, prevHeld, prevXLog
		vkmsSettleWatch = prevWatch
	})
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

// vkms passed the check but the X server still failed on it after release:
// the host takes vkms away from X and unloads it, records the denial, and
// does not force a spare connector in the same pass. Changing the X server's
// displays again right after a failure is what froze a session (Linux 7.0,
// xe): the forced connector was being set up while X still held the failed
// vkms screen.
func TestVkmsFailingInXAfterReleaseIsTakenAwayWithoutFallback(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	withGuardTest(t)
	t.Setenv(vkmsAutoloadEnv, "")
	vkmsPostLoadCheck = func() bool {
		displayGuardDenyUntil(guardMethodVkms, "the kernel log shows a vkms failure (vkms_prepare_fb)", true)
		return true
	}
	unloads := 0
	vkmsUnloadHeldFn = func(bool) error { unloads++; return nil }
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
	if unloads != 1 {
		t.Fatalf("vkms was not taken away from the X server (unloads = %d)", unloads)
	}
	if forced != 0 {
		t.Fatal("a spare connector was forced in the same pass as a vkms failure in X")
	}
	if _, denied := displayGuardDenied(guardMethodVkms); !denied {
		t.Fatal("vkms must be denied after the failure")
	}
	vkmsSetup.mu.Lock()
	vkmsSetup.lastFail = time.Time{}
	vkmsSetup.mu.Unlock()
	ensureX11VirtualDisplay("again", true)
	if calls.Load() != 1 {
		t.Fatalf("vkms was loaded again after the failure (%d calls)", calls.Load())
	}
	if forced == 0 {
		t.Fatal("the next pass must use the spare connector")
	}
}

// The X log shows the failed mode set even when the kernel's one-time WARN
// was already printed earlier in the boot.
func TestVkmsFailureInXLogTriggersRecovery(t *testing.T) {
	withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	withGuardTest(t)
	t.Setenv(vkmsAutoloadEnv, "")
	vkmsXLogFailureFn = func(xorgLogMark) bool { return true }
	unloads := 0
	vkmsUnloadHeldFn = func(bool) error { unloads++; return nil }
	ensureX11VirtualDisplay("test", true)
	if unloads != 1 {
		t.Fatalf("unloads = %d, want 1", unloads)
	}
	if _, denied := displayGuardDenied(guardMethodVkms); !denied {
		t.Fatal("vkms must be denied")
	}
}

// The check refuses vkms: X never sees it (the fake output is not added),
// vkms is unloaded and denied for the kernel series, and the spare connector
// is used in the same pass, since the X server was never touched.
func TestVkmsRefusedByCheckIsNeverShownToX(t *testing.T) {
	f, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	withGuardTest(t)
	t.Setenv(vkmsAutoloadEnv, "")
	outputsBefore := len(f.outputs)
	vkmsLoadPrivileged = func(bool) error { calls.Add(1); return nil } // held: X sees nothing
	vkmsVerifyFn = func(bool) error {
		return &vkmsScanoutError{GPU: "/dev/dri/card1", Step: "setting a mode", Err: unix.EOPNOTSUPP}
	}
	unloads := 0
	vkmsUnloadHeldFn = func(bool) error { unloads++; return nil }
	fakeSysfsDRM(t, "disconnected", nil)
	forced := 0
	forceConnectorPrivileged = func(path, value string, _ bool) error {
		forced++
		return os.WriteFile(path, []byte("connected\n"), 0o644)
	}
	ensureX11VirtualDisplay("test", true)
	if len(f.outputs) != outputsBefore {
		t.Fatal("the X server was given a vkms output that failed the check")
	}
	if unloads != 1 {
		t.Fatalf("unloads = %d, want 1", unloads)
	}
	if _, denied := displayGuardDenied(guardMethodVkms); !denied {
		t.Fatal("vkms must be denied after the check refused it")
	}
	if forced == 0 {
		t.Fatal("the spare connector must be tried in the same pass")
	}
}

// A check that could not run (not a verdict on the kernel) neither shows vkms
// to X nor denies it for good.
func TestVkmsUncheckedIsNotShownButNotDenied(t *testing.T) {
	_, calls := withVkmsSetupTest(t, false, true, func(*fakeXrandr) error { return nil })
	withGuardTest(t)
	t.Setenv(vkmsAutoloadEnv, "")
	vkmsLoadPrivileged = func(bool) error { calls.Add(1); return nil }
	vkmsVerifyFn = func(bool) error { return errVkmsProbeNotMaster }
	unloads := 0
	vkmsUnloadHeldFn = func(bool) error { unloads++; return nil }
	ensureX11VirtualDisplay("test", true)
	if unloads != 1 {
		t.Fatalf("unloads = %d, want 1", unloads)
	}
	if x11VirtualDisplayReady() {
		t.Fatal("an unchecked vkms reached the X server")
	}
	if _, denied := displayGuardDenied(guardMethodVkms); denied {
		t.Fatal("an inconclusive check must not deny vkms for good")
	}
}

// A vkms left loaded and held by an earlier, unfinished check is checked
// again instead of being treated as in use by X.
func TestHeldVkmsIsCheckedAgain(t *testing.T) {
	_, calls := withVkmsSetupTest(t, true, true, func(*fakeXrandr) error { return nil })
	vkmsIsHeld = func() bool { return true }
	verified := 0
	vkmsVerifyFn = func(bool) error { verified++; return nil }
	ensureX11VirtualDisplay("test", true)
	if calls.Load() != 1 || verified != 1 {
		t.Fatalf("load calls = %d, checks = %d; want 1 and 1", calls.Load(), verified)
	}
}

func TestVkmsHeldFromXReadsTheUdevDatabase(t *testing.T) {
	sys, db := t.TempDir(), t.TempDir()
	prevSys, prevDB := sysfsDRMDir, udevDataDir
	sysfsDRMDir, udevDataDir = sys, db
	t.Cleanup(func() { sysfsDRMDir, udevDataDir = prevSys, prevDB })
	if err := os.MkdirAll(filepath.Join(sys, "card0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sys, "card0", "dev"), []byte("226:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(db, "c226:0"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("E:DEVTYPE=drm_minor\nG:seat\n")
	if vkmsHeldFromX("/dev/dri/card0") {
		t.Fatal("an unheld card reported as held")
	}
	write("E:DEVTYPE=drm_minor\nE:ID_SEAT=drawsplay-hold\nG:seat\n")
	if !vkmsHeldFromX("/dev/dri/card0") {
		t.Fatal("a held card reported as unheld")
	}
}

func TestXorgLogFailureOnlyCountsNewLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DISPLAY", ":7")
	logPath := filepath.Join(home, ".local", "share", "xorg", "Xorg.7.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "[   101.571] (EE) modeset(G0): failed to set mode: Operation not supported\n"
	if err := os.WriteFile(logPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	mark := markXorgLogs()
	if xorgLogShowsVkmsFailureSince(mark) {
		t.Fatal("a failure logged before the mark was counted")
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("[   201.000] (II) modeset(0): Allocate new frame buffer 2944x1200 stride\n")
	_ = f.Close()
	if xorgLogShowsVkmsFailureSince(mark) {
		t.Fatal("an unrelated line was counted")
	}
	f, _ = os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("[   201.050] (EE) modeset(G0): failed to set mode: Operation not supported\n")
	_ = f.Close()
	if !xorgLogShowsVkmsFailureSince(mark) {
		t.Fatal("a new failed mode set on a GPU screen was missed")
	}
}
