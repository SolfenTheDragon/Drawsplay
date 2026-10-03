package main

import (
	"testing"
	"time"
)

func TestThrottledLogAllowedSuppressesWithinInterval(t *testing.T) {
	key := "test-throttle-key"
	base := time.Unix(1000, 0)

	if suppressed, ok := throttledLogAllowed(key, base); !ok || suppressed != 0 {
		t.Fatalf("first occurrence = (%d, %v), want (0, true)", suppressed, ok)
	}
	for i := 0; i < 5; i++ {
		if _, ok := throttledLogAllowed(key, base.Add(time.Duration(i+1)*time.Second)); ok {
			t.Fatalf("occurrence %d inside interval was not suppressed", i+1)
		}
	}
	suppressed, ok := throttledLogAllowed(key, base.Add(throttledLogInterval))
	if !ok || suppressed != 5 {
		t.Fatalf("after interval = (%d, %v), want (5, true)", suppressed, ok)
	}
	// Counter resets after being reported.
	if suppressed, ok := throttledLogAllowed(key, base.Add(2*throttledLogInterval)); !ok || suppressed != 0 {
		t.Fatalf("next window = (%d, %v), want (0, true)", suppressed, ok)
	}
}

func TestThrottledLogAllowedIndependentKeys(t *testing.T) {
	base := time.Unix(2000, 0)
	if _, ok := throttledLogAllowed("key-a", base); !ok {
		t.Fatal("key-a first occurrence suppressed")
	}
	if _, ok := throttledLogAllowed("key-b", base); !ok {
		t.Fatal("key-b first occurrence suppressed despite independent key")
	}
}
