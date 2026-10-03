package main

import (
	"testing"
	"time"
)

func TestIsDroppableNonRefNAL(t *testing.T) {
	nonRefP := []byte{0, 0, 0, 1, 0x01, 0xAA}
	refP := []byte{0, 0, 0, 1, 0x41, 0xAA}
	idr := []byte{0, 0, 0, 1, 0x65, 0xAA}
	if !isDroppableNonRefNAL(nonRefP) {
		t.Fatal("expected non-reference P-slice to be droppable")
	}
	if isDroppableNonRefNAL(refP) {
		t.Fatal("reference P-slice must not be droppable")
	}
	if isDroppableNonRefNAL(idr) {
		t.Fatal("IDR must not be droppable")
	}
}

func TestTemporalControllerTransitions(t *testing.T) {
	base := time.Unix(0, 0)
	now := base
	tc := &temporalController{
		now:        func() time.Time { return now },
		dropNonRef: false,
		cleanSince: now,
	}
	if !tc.onCongestion() || !tc.droppingNonRef() {
		t.Fatal("first congestion should enable non-ref dropping")
	}
	if tc.onCongestion() {
		t.Fatal("repeated congestion should not report a new runtime transition")
	}
	now = now.Add(stepUpAfter - time.Second)
	if tc.onClean() {
		t.Fatal("should not restore temporal quality before clean interval")
	}
	now = now.Add(2 * time.Second)
	if !tc.onClean() || tc.droppingNonRef() {
		t.Fatal("should restore temporal quality after clean interval")
	}
}
