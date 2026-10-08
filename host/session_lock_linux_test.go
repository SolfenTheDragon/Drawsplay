//go:build linux

package main

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestQuerySessionLockedOnCurrentSessionBus(t *testing.T) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Skipf("no session D-Bus: %v", err)
	}
	defer conn.Close()

	_, err = querySessionLocked(t.Context(), conn)
	if err != nil {
		t.Skipf("no supported screen-saver service in this test session: %v", err)
	}
}

func TestApplySessionLockState(t *testing.T) {
	previous := sessionDisplayBlackout()
	t.Cleanup(func() { setSessionDisplayBlackout(previous) })

	var last bool
	haveLast := false
	if !applySessionLockState(false, &last, &haveLast) {
		t.Fatal("initial lock state was not applied")
	}
	if sessionDisplayBlackout() {
		t.Fatal("unlocked session enabled the blackout")
	}
	if applySessionLockState(false, &last, &haveLast) {
		t.Fatal("unchanged lock state was applied twice")
	}
	if !applySessionLockState(true, &last, &haveLast) {
		t.Fatal("lock transition was not applied")
	}
	if !sessionDisplayBlackout() {
		t.Fatal("locked session did not enable the blackout")
	}
	if !applySessionLockState(false, &last, &haveLast) {
		t.Fatal("unlock transition was not applied")
	}
	if sessionDisplayBlackout() {
		t.Fatal("unlocked session kept the blackout enabled")
	}
}

func TestScreenSaverServicesIncludeCommonLinuxDesktops(t *testing.T) {
	want := map[string]bool{
		"org.freedesktop.ScreenSaver": false,
		"org.cinnamon.ScreenSaver":    false,
		"org.gnome.ScreenSaver":       false,
		"org.mate.ScreenSaver":        false,
	}
	for _, service := range screenSaverServices {
		if _, ok := want[service.name]; ok {
			want[service.name] = true
		}
		if service.path == "" || service.interfaceName == "" {
			t.Errorf("incomplete screen-saver service: %+v", service)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing screen-saver service %s", name)
		}
	}
}
