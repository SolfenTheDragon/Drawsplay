package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestSelectADBTargets(t *testing.T) {
	serials := []string{"tablet", "phone"}
	if got, err := selectADBTargets(serials, "tablet", false); err != nil || !reflect.DeepEqual(got, []string{"tablet"}) {
		t.Fatalf("preferred target = %v, %v", got, err)
	}
	if got, err := selectADBTargets(serials, "", false); err != nil || !reflect.DeepEqual(got, serials) {
		t.Fatalf("all targets = %v, %v", got, err)
	}
	if _, err := selectADBTargets(serials, "missing", false); err == nil {
		t.Error("missing preferred serial unexpectedly accepted")
	}
	if _, err := selectADBTargets(nil, "", false); err == nil {
		t.Error("empty device list unexpectedly accepted")
	}
	if _, err := selectADBTargets(serials, "", true); err == nil {
		t.Error("strict serial mode unexpectedly accepted empty preferred serial")
	}
}

func TestExponentialRetryDelayIsBounded(t *testing.T) {
	for failures := 0; failures < 10; failures++ {
		delay := exponentialRetryDelay(failures)
		shift := failures
		if shift > 4 {
			shift = 4
		}
		minimum := adbReverseRetryDelay << shift
		if minimum > adbReverseMaxRetryDelay {
			minimum = adbReverseMaxRetryDelay
		}
		if delay < minimum {
			t.Fatalf("delay below exponential floor at failure %d: %s < %s", failures, delay, minimum)
		}
		if delay > adbReverseMaxRetryDelay+adbReverseMaxRetryDelay/4 {
			t.Fatalf("delay exceeded jittered cap: %s", delay)
		}
	}
}

func TestRetryDelayForADBErrorKeepsFastProbeForAttachStates(t *testing.T) {
	cases := []error{
		errNoAuthorizedADBDevices,
		errPreferredADBUnavailable,
		errADBDeviceUnauthorized,
		errADBDeviceNoPermissions,
		fmt.Errorf("wrapped: %w", errNoAuthorizedADBDevices),
	}
	for _, err := range cases {
		delay := retryDelayForADBError(err, 7)
		if delay != adbReverseRetryDelay {
			t.Fatalf("retryDelayForADBError(%v) = %s, want %s", err, delay, adbReverseRetryDelay)
		}
	}
}

func TestRetryDelayForADBErrorBacksOffForOtherFailures(t *testing.T) {
	delay := retryDelayForADBError(errors.New("adb command failed"), 3)
	if delay < 16*time.Second {
		t.Fatalf("retryDelayForADBError did not back off: %s", delay)
	}
}

func TestShouldRestartADBServer(t *testing.T) {
	if shouldRestartADBServer(errNoAuthorizedADBDevices, 10) {
		t.Fatal("unexpected restart for no-device state")
	}
	if shouldRestartADBServer(errPreferredADBUnavailable, 10) {
		t.Fatal("unexpected restart for preferred-missing state")
	}
	if shouldRestartADBServer(errADBDeviceUnauthorized, 10) {
		t.Fatal("unexpected restart for unauthorized-device state")
	}
	if shouldRestartADBServer(errADBDeviceNoPermissions, 10) {
		t.Fatal("unexpected restart for adb permission state")
	}
	if shouldRestartADBServer(errors.New("adb I/O failure"), 2) {
		t.Fatal("unexpected restart before failure threshold")
	}
	if !shouldRestartADBServer(errors.New("adb I/O failure"), 5) {
		t.Fatal("expected restart at threshold")
	}
}

func TestParseADBDevicesOutput(t *testing.T) {
	output := `
List of devices attached
R58M123ABC	device usb:3-2 product:foo model:bar device:baz transport_id:2
adb-12345._adb-tls-connect._tcp	device product:_adb-tls-connect._tcp
0123456789ABCDEF	unauthorized usb:1-1 transport_id:4
ZX1G22BQQ	no permissions (user in plugdev group; are your udev rules wrong?); see [http://developer.android.com/tools/device.html]
`
	serials, unauthorized, noPermissions := parseADBDevicesOutput(output)
	if !reflect.DeepEqual(serials, []string{"R58M123ABC"}) {
		t.Fatalf("serials = %v, want [R58M123ABC]", serials)
	}
	if !unauthorized {
		t.Fatal("unauthorized state not detected")
	}
	if !noPermissions {
		t.Fatal("no-permissions state not detected")
	}
}
