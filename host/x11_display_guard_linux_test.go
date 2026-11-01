//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func init() {
	// Tests must never read the real kernel log or the user's guard file.
	vkmsKernelLogFn = func() bool { return false }
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
	kernelReleaseFn = func() string { return "7.0.0-1018-oem" }
	if _, denied := displayGuardDenied(guardMethodVkms); denied {
		t.Fatal("a kernel update must end the denial")
	}
}
