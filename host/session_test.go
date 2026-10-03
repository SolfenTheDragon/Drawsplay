package main

import (
	"sync"
	"testing"
)

func TestSessionOwnershipKeepsInputAndVideoOnOneTransport(t *testing.T) {
	var ownership sessionOwnership
	if !ownership.acquireInput("usb") {
		t.Fatal("USB input did not acquire an idle session")
	}
	if !ownership.reserveVideo("usb") {
		t.Fatal("USB video did not reserve the USB input session")
	}
	if active, _ := ownership.activateReservedVideo("usb"); !active {
		t.Fatal("USB video did not join the USB input session")
	}
	if ownership.acquireInput("wifi") || ownership.reserveVideo("wifi") {
		t.Fatal("Wi-Fi joined a session owned by USB")
	}

	ownership.releaseInput("usb")
	if got := ownership.currentTransport(); got != "usb" {
		t.Fatalf("owner after input release = %q, want usb while video is active", got)
	}
	ownership.releaseVideo("usb")
	if got := ownership.currentTransport(); got != "" {
		t.Fatalf("owner after final channel release = %q, want idle", got)
	}
	if !ownership.acquireInput("wifi") {
		t.Fatal("Wi-Fi input did not acquire the released session")
	}
}

func TestSessionOwnershipReservesTransportAcrossVideoHandoff(t *testing.T) {
	var ownership sessionOwnership
	if !ownership.reserveVideo("usb") {
		t.Fatal("initial USB video did not reserve the session")
	}
	if active, _ := ownership.activateReservedVideo("usb"); !active {
		t.Fatal("initial USB video did not acquire the session")
	}
	if !ownership.reserveVideo("usb") {
		t.Fatal("same-transport video handoff was not reserved")
	}

	active, handoff := ownership.activateReservedVideo("usb")
	if active || handoff == nil {
		t.Fatal("active video did not return a handoff notification")
	}
	ownership.releaseVideo("usb")
	select {
	case <-handoff:
	default:
		t.Fatal("video release did not notify the handoff waiter")
	}
	if ownership.reserveVideo("wifi") {
		t.Fatal("Wi-Fi acquired ownership during the USB handoff gap")
	}
	if active, _ := ownership.activateReservedVideo("usb"); !active {
		t.Fatal("reserved USB handoff did not activate")
	}
	if ownership.reserveVideo("usb") {
		t.Fatal("late reconnect overlapped the completing handoff")
	}
	ownership.finishVideoHandoff("usb")
	if !ownership.reserveVideo("usb") {
		t.Fatal("reconnect was not accepted after the handoff completed")
	}
	ownership.cancelVideoReservation("usb")
	ownership.releaseVideo("usb")
	if got := ownership.currentTransport(); got != "" {
		t.Fatalf("owner after handoff release = %q, want idle", got)
	}
}

func TestSessionOwnershipRejectsOverlappingVideoWaiters(t *testing.T) {
	var ownership sessionOwnership
	if !ownership.reserveVideo("usb") {
		t.Fatal("initial USB video did not reserve the session")
	}
	if active, _ := ownership.activateReservedVideo("usb"); !active {
		t.Fatal("initial USB video did not acquire the session")
	}
	if !ownership.reserveVideo("usb") {
		t.Fatal("first USB handoff waiter was rejected")
	}
	if ownership.reserveVideo("usb") {
		t.Fatal("second USB handoff waiter was accepted")
	}
}

func TestSessionOwnershipCancelledHandoffDoesNotStrandOwner(t *testing.T) {
	for _, releaseFirst := range []bool{false, true} {
		name := "cancel-before-release"
		if releaseFirst {
			name = "release-before-cancel"
		}
		t.Run(name, func(t *testing.T) {
			var ownership sessionOwnership
			if !ownership.reserveVideo("usb") {
				t.Fatal("initial USB video did not reserve the session")
			}
			if active, _ := ownership.activateReservedVideo("usb"); !active {
				t.Fatal("initial USB video did not acquire the session")
			}
			if !ownership.reserveVideo("usb") {
				t.Fatal("USB handoff waiter was rejected")
			}
			if active, handoff := ownership.activateReservedVideo("usb"); active || handoff == nil {
				t.Fatal("active video did not return a handoff notification")
			}

			if releaseFirst {
				ownership.releaseVideo("usb")
				ownership.cancelVideoReservation("usb")
			} else {
				ownership.cancelVideoReservation("usb")
				ownership.releaseVideo("usb")
			}
			if got := ownership.currentTransport(); got != "" {
				t.Fatalf("owner after cancelled handoff = %q, want idle", got)
			}
			if !ownership.reserveVideo("wifi") {
				t.Fatal("Wi-Fi could not acquire ownership after cancelled handoff")
			}
		})
	}
}

func TestSessionOwnershipSerializesCrossTransportChannelRace(t *testing.T) {
	var ownership sessionOwnership
	start := make(chan struct{})
	results := make([]bool, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		results[0] = ownership.acquireInput("usb")
	}()
	go func() {
		defer wg.Done()
		<-start
		if ownership.reserveVideo("wifi") {
			results[1], _ = ownership.activateReservedVideo("wifi")
		}
	}()
	close(start)
	wg.Wait()

	if results[0] == results[1] {
		t.Fatalf("race results = %v, want exactly one transport owner", results)
	}
	if results[0] && ownership.currentTransport() != "usb" {
		t.Fatalf("USB input won but owner = %q", ownership.currentTransport())
	}
	if results[1] && ownership.currentTransport() != "wifi" {
		t.Fatalf("Wi-Fi video won but owner = %q", ownership.currentTransport())
	}
}
