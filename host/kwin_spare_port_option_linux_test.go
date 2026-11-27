//go:build linux

package main

import (
	"errors"
	"testing"
)

var errTestNoKWin = errors.New("no KWin in tests")

// stubKWinCreate makes Create's virtual-output path fail at once and records
// whether it tried the spare port.
func stubKWinCreate(t *testing.T, wanted bool) (triedPort *bool) {
	t.Helper()
	tried := false
	oldWanted, oldStart, oldPrepare, oldDial := kwinSpareConnectorWanted, kwinStartConnector, kwinPrepare, kwinDial
	oldRefused, oldFailed := kwinConnectorRefused, kwinPortFailed
	kwinSpareConnectorWanted = func() bool { return wanted }
	kwinStartConnector = func(int, int, bool) (wlExtHandle, error) {
		tried = true
		return nil, errNoSpareConnector
	}
	kwinPrepare = func() bool { return false }
	kwinDial = func() (*wlConn, error) { return nil, errTestNoKWin }
	t.Cleanup(func() {
		kwinSpareConnectorWanted, kwinStartConnector, kwinPrepare, kwinDial = oldWanted, oldStart, oldPrepare, oldDial
		kwinConnectorRefused, kwinPortFailed = oldRefused, oldFailed
	})
	return &tried
}

// The virtual output is the default: the spare port is never touched.
func TestKWinCreateUsesTheVirtualOutputByDefault(t *testing.T) {
	tried := stubKWinCreate(t, false)
	if _, err := (kwinBackend{}).Create(1920, 1200); !errors.Is(err, errTestNoKWin) {
		t.Fatalf("err = %v, want the virtual output's", err)
	}
	if *tried {
		t.Fatal("tried the spare HDMI port although the option is off")
	}
}

// With the option on the port comes first, and the virtual output is still
// the fallback.
func TestKWinCreateTriesTheSparePortWhenAsked(t *testing.T) {
	tried := stubKWinCreate(t, true)
	if _, err := (kwinBackend{}).Create(1920, 1200); !errors.Is(err, errTestNoKWin) {
		t.Fatalf("err = %v, want the virtual output's fallback", err)
	}
	if !*tried {
		t.Fatal("did not try the spare HDMI port")
	}
}

// Turning the option off forgets a dismissed password prompt, so turning it
// on again asks again.
func TestKWinOptionOffForgetsADismissedPrompt(t *testing.T) {
	stubKWinCreate(t, false)
	kwinConnectorRefused, kwinPortFailed = errKWinConnectorDeclined, true
	(kwinBackend{}).Create(1920, 1200)
	if kwinConnectorRefused != nil || kwinPortFailed {
		t.Fatal("the dismissed prompt outlived the option")
	}
}

func TestKWinKeepsOnlyTheOutputKindAsked(t *testing.T) {
	port, virtual := &kwinConnectorOutput{}, &kwinOutput{}
	for _, c := range []struct {
		wanted     bool
		hd         wlExtHandle
		keep       bool
		whatHeldIs string
	}{
		{false, virtual, true, "virtual output, option off"},
		{false, port, false, "port, option off"},
		{true, port, true, "port, option on"},
		{true, virtual, false, "virtual output, option on"},
	} {
		stubKWinCreate(t, c.wanted)
		kwinPortFailed = false
		if got := wlExtKeeps(kwinBackend{}, c.hd); got != c.keep {
			t.Errorf("%s: keeps = %v, want %v", c.whatHeldIs, got, c.keep)
		}
	}
}

// Backends that do not say keep every output (sway, Hyprland).
func TestWlExtKeepsWithoutAnOpinion(t *testing.T) {
	if !wlExtKeeps(swayBackend{}, &kwinOutput{}) || !wlExtKeeps(hyprBackend{}, &kwinOutput{}) {
		t.Fatal("a backend without Keeps dropped its output")
	}
}

func TestKDESpareHDMIPortIsOffByDefaultAndRemakesTheDisplay(t *testing.T) {
	d := defaultSettings()
	if d.KDESpareHDMIPort {
		t.Fatal("the spare HDMI port must be opt-in")
	}
	next := d
	next.KDESpareHDMIPort = true
	if !videoSettingsChanged(d, next) {
		t.Fatal("toggling the option does not restart the video session")
	}
}

// With the option on but the port unusable, the virtual output standing in
// is kept across sessions like any held output, not made again (and the port
// tried again) each time.
func TestKWinKeepsTheStandInVirtualOutput(t *testing.T) {
	tried := stubKWinCreate(t, true)
	(kwinBackend{}).Create(1920, 1200)
	if !*tried || !kwinPortFailed {
		t.Fatal("the failed port was not noted")
	}
	if !wlExtKeeps(kwinBackend{}, &kwinOutput{}) {
		t.Fatal("the stand-in virtual output is replaced every session")
	}
}
