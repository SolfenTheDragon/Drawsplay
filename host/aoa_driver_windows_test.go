//go:build windows

package main

import (
	"testing"
	"time"
)

func TestResetWinUSBInstallAttemptsAllowsAnotherTry(t *testing.T) {
	t.Cleanup(func() { winusbInstallAttempts.Clear(); winusbRetryUntil.Store(0) })
	winusbInstallAttempts.Store("04e8:6860", struct{}{})
	resetWinUSBInstallAttempts()
	if _, loaded := winusbInstallAttempts.LoadOrStore("04e8:6860", struct{}{}); loaded {
		t.Fatal("Retry should clear earlier WinUSB attempts")
	}
	now := time.Now()
	// Both tablet identities (normal, then accessory mode) fall in the window.
	if !winusbRetryActive(now) || !winusbRetryActive(now.Add(winusbRetryWindow/2)) {
		t.Fatal("Retry should let a portable host prompt again for a while")
	}
	if winusbRetryActive(now.Add(winusbRetryWindow + time.Second)) {
		t.Fatal("the Retry window must expire so later plug-ins do not prompt unasked")
	}
}
