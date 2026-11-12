//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	// Tests must never read the real kernel log or the user's guard file.
	vkmsKernelLogFn = func() bool { return false }
	vkmsPostLoadCheck = func() bool { return false }
	readKernelLogFn = func() (string, bool) { return "", false }
	displayGuardDir = func() (string, error) { return os.TempDir() + "/drawsplay-guard-test-unused", nil }
}

func withGuardTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	oldDir, oldLog, oldUndo, oldNow := displayGuardDir, vkmsKernelLogFn, undoForcedOutput, displayGuardNow
	displayGuardDir = func() (string, error) { return dir, nil }
	vkmsKernelLogFn = func() bool { return false }
	undoForcedOutput = func() {}
	t.Cleanup(func() {
		displayGuardDir, vkmsKernelLogFn, undoForcedOutput, displayGuardNow = oldDir, oldLog, oldUndo, oldNow
	})
	t.Setenv(vkmsAutoloadEnv, "")
}

func TestDisplayGuardCleanStepLeavesNoState(t *testing.T) {
	withGuardTest(t)
	displayGuardBegin(guardMethodVkms)
	displayGuardEnd(guardMethodVkms)
	recoverDisplayGuard()
	if _, denied := displayGuardDenied(guardMethodVkms); denied {
		t.Fatal("a finished step must not deny the method")
	}
	path, _ := displayGuardPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file should be gone, stat err = %v", err)
	}
}

func TestDisplayGuardInterruptedStepDeniesMethodOnNextStart(t *testing.T) {
	withGuardTest(t)
	displayGuardBegin(guardMethodForcedConnector) // process dies here
	undone := false
	undoForcedOutput = func() { undone = true }
	recoverDisplayGuard()
	if _, denied := displayGuardDenied(guardMethodForcedConnector); !denied {
		t.Fatal("an interrupted step must deny the method")
	}
	if !undone {
		t.Fatal("a forced connector must be released on recovery")
	}
	if _, denied := displayGuardDenied(guardMethodVkms); denied {
		t.Fatal("an unrelated method must stay allowed")
	}
	// Recovery is idempotent: the mark is consumed.
	recoverDisplayGuard()
}

func TestDisplayGuardDenialExpires(t *testing.T) {
	withGuardTest(t)
	displayGuardDeny(guardMethodVkms, "test")
	displayGuardNow = func() time.Time { return time.Now().Add(displayGuardDenialTTL + time.Hour) }
	if _, denied := displayGuardDenied(guardMethodVkms); denied {
		t.Fatal("denial should expire")
	}
}

func TestDisplayGuardCorruptFileIsIgnored(t *testing.T) {
	withGuardTest(t)
	path, _ := displayGuardPath()
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	recoverDisplayGuard()
	if _, denied := displayGuardDenied(guardMethodVkms); denied {
		t.Fatal("corrupt state must read as empty")
	}
	_ = filepath.Base(path)
}

func TestVkmsBlockedByKernelLogUnlessOptedIn(t *testing.T) {
	withGuardTest(t)
	vkmsKernelLogFn = func() bool { return true }
	if !vkmsBlocked() {
		t.Fatal("vkms must be blocked once the kernel log shows the failure")
	}
	t.Setenv(vkmsAutoloadEnv, "1")
	if vkmsBlocked() {
		t.Fatal("the opt-in must override the block")
	}
}

func TestVkmsBlockedByDenial(t *testing.T) {
	withGuardTest(t)
	displayGuardDeny(guardMethodVkms, "test")
	if !vkmsBlocked() {
		t.Fatal("a denied vkms must be blocked")
	}
}

func TestPickX11VirtualOutputSkipsBlockedVkms(t *testing.T) {
	withGuardTest(t)
	st := xrandrState{Outputs: []xrandrOutput{
		{Name: "eDP-1", Connected: true, Active: true, Primary: true},
		{Name: "Virtual-1-1", Connected: true},
	}}
	if _, err := pickX11VirtualOutput(st, ""); err != nil {
		t.Fatalf("vkms output should be picked while healthy: %v", err)
	}
	vkmsKernelLogFn = func() bool { return true }
	if _, err := pickX11VirtualOutput(st, ""); err == nil {
		t.Fatal("a blocked vkms output must not be picked")
	}
	// An explicitly chosen output is the user's call.
	if _, err := pickX11VirtualOutput(st, "Virtual-1-1"); err != nil {
		t.Fatalf("explicit output must still work: %v", err)
	}
}

func TestKernelLogShowsVkmsFailure(t *testing.T) {
	bad := "kernel: WARNING: drivers/dma-buf/dma-buf.c:1632 at dma_buf_vmap\nkernel:  vkms_prepare_fb+0x50/0x60 [vkms]\n"
	if !kernelLogShowsVkmsFailure(bad) {
		t.Fatal("trace not recognised")
	}
	if kernelLogShowsVkmsFailure("kernel: [drm] Initialized vkms 1.0.0\n") {
		t.Fatal("a clean vkms init is not a failure")
	}
}

func TestKernelLogDenialLastsUntilKernelChanges(t *testing.T) {
	withGuardTest(t)
	old := kernelReleaseFn
	t.Cleanup(func() { kernelReleaseFn = old })
	kernelReleaseFn = func() string { return "7.0.0-1017-oem" }
	displayGuardDenyUntil(guardMethodVkms, "kernel log", true)
	displayGuardNow = func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }
	if _, denied := displayGuardDenied(guardMethodVkms); !denied {
		t.Fatal("a kernel-proven failure must outlast the usual expiry on the same kernel")
	}
	// A point update ships the same DRM code: the display must not be
	// taken down again to find that out.
	kernelReleaseFn = func() string { return "7.0.0-1018-oem" }
	if _, denied := displayGuardDenied(guardMethodVkms); !denied {
		t.Fatal("a kernel point update must keep the denial")
	}
	kernelReleaseFn = func() string { return "7.1.0-1001-oem" }
	if _, denied := displayGuardDenied(guardMethodVkms); denied {
		t.Fatal("a new kernel series must end the denial")
	}
}

func TestKernelSeries(t *testing.T) {
	for in, want := range map[string]string{
		"7.0.0-1018-oem":    "7.0",
		"7.0.0-1017-oem":    "7.0",
		"6.14.0-29-generic": "6.14",
		"7.1-rc3":           "7.1",
		"7":                 "7",
		"":                  "",
	} {
		if got := kernelSeries(in); got != want {
			t.Errorf("kernelSeries(%q) = %q, want %q", in, got, want)
		}
	}
}

// withKernelLogTest wires readKernelLogFn to text and resets the cache.
func withKernelLogTest(t *testing.T, text *string) {
	t.Helper()
	withGuardTest(t)
	vkmsKernelLogFn = vkmsKernelLogShowsFailure
	oldRead := readKernelLogFn
	readKernelLogFn = func() (string, bool) { return *text, true }
	vkmsLogCache.mu.Lock()
	vkmsLogCache.checked, vkmsLogCache.failed = time.Time{}, false
	vkmsLogCache.mu.Unlock()
	t.Cleanup(func() {
		readKernelLogFn = oldRead
		vkmsLogCache.mu.Lock()
		vkmsLogCache.checked, vkmsLogCache.failed = time.Time{}, false
		vkmsLogCache.mu.Unlock()
	})
}

func TestVkmsKernelLogRecheckBypassesTheCleanCache(t *testing.T) {
	text := "kernel: [drm] Initialized vkms 1.0.0\n"
	withKernelLogTest(t, &text)
	if vkmsKernelLogShowsFailure() {
		t.Fatal("a clean log must not block vkms")
	}
	// The failure arrives a second after the load, well inside the cache window.
	text += "kernel:  vkms_prepare_fb+0x50/0x60 [vkms]\n"
	if vkmsKernelLogShowsFailure() {
		t.Fatal("the once-a-minute check is expected to still report the cached result")
	}
	if !vkmsKernelLogRecheck() {
		t.Fatal("the post-load recheck must read the log again")
	}
	if !vkmsKernelLogShowsFailure() {
		t.Fatal("a failure must stick")
	}
	reason, denied := displayGuardDenied(guardMethodVkms)
	if !denied || !strings.Contains(reason, "vkms_prepare_fb") {
		t.Fatalf("the failure must be recorded as a denial, got %q, %v", reason, denied)
	}
}

func TestVkmsKernelLogPreviousBootFailureBlocksBeforeLoading(t *testing.T) {
	// The module is not loaded in this boot (the user rebooted after the
	// freeze), but the previous boot's log carries the trace.
	text := "kernel: Linux version 7.0.0-1018-oem\n\nkernel:  vkms_prepare_fb+0x50/0x60 [vkms]\n"
	withKernelLogTest(t, &text)
	if !vkmsBlocked() {
		t.Fatal("a failure in the previous boot's log must block vkms before it is loaded again")
	}
}

func TestVkmsKernelLogUnreadableIsNotCached(t *testing.T) {
	text := ""
	withKernelLogTest(t, &text)
	reads := 0
	readKernelLogFn = func() (string, bool) { reads++; return "", false }
	vkmsKernelLogShowsFailure()
	vkmsKernelLogShowsFailure()
	if reads != 2 {
		t.Fatalf("an unreadable log must be retried, reads = %d", reads)
	}
}
