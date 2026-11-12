package main

import (
	"testing"
	"time"
)

func TestHeldOutputRemovesOnlyAfterGraceWithoutANewClaim(t *testing.T) {
	var h heldOutput
	removed := make(chan struct{}, 2)
	remove := func() { removed <- struct{}{} }

	h.mu.Lock()
	first, _ := h.claimLocked()
	if !h.unclaimLocked(first) {
		t.Fatal("the latest session could not release its claim")
	}
	h.afterLocked(first, 20*time.Millisecond, remove)
	// A reconnect within the grace period keeps the output.
	second, _ := h.claimLocked()
	h.mu.Unlock()
	select {
	case <-removed:
		t.Fatal("removed although a new session claimed the output")
	case <-time.After(60 * time.Millisecond):
	}

	h.mu.Lock()
	if h.unclaimLocked(first) {
		t.Fatal("an older session released the newer session's claim")
	}
	h.unclaimLocked(second)
	h.afterLocked(second, time.Millisecond, remove)
	h.mu.Unlock()
	select {
	case <-removed:
	case <-time.After(time.Second):
		t.Fatal("not removed after the grace period")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.exitLocked()
	if _, err := h.claimLocked(); err != errHostExiting {
		t.Fatalf("claim while exiting: %v", err)
	}
	if err := h.recoverableLocked(second, "test output"); err != errHostExiting {
		t.Fatalf("recover while exiting: %v", err)
	}
}
