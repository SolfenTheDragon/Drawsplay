//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMergeEDIDOverride(t *testing.T) {
	for _, c := range []struct{ cur, conn, file, want string }{
		{"", "HDMI-A-1", "drawsplay-1920x1200.bin", "HDMI-A-1:edid/drawsplay-1920x1200.bin"},
		{"\x00", "HDMI-A-1", "", ""},
		// The user's entries stay, after the host's.
		{"DP-1:edid/mine.bin\n", "HDMI-A-1", "drawsplay-1920x1200.bin", "HDMI-A-1:edid/drawsplay-1920x1200.bin,DP-1:edid/mine.bin"},
		{"HDMI-A-1:edid/drawsplay-1920x1200.bin,DP-1:edid/mine.bin", "HDMI-A-1", "", "DP-1:edid/mine.bin"},
		// A leftover for another port goes too; a fallback entry stays.
		{"HDMI-A-2:edid/drawsplay-2560x1600.bin,edid/all.bin", "HDMI-A-1", "drawsplay-1920x1200.bin", "HDMI-A-1:edid/drawsplay-1920x1200.bin,edid/all.bin"},
		// So does the user's entry for the same port only while the host's
		// comes first: the kernel uses the first match.
		{"HDMI-A-1:edid/theirs.bin", "HDMI-A-1", "drawsplay-1920x1200.bin", "HDMI-A-1:edid/drawsplay-1920x1200.bin,HDMI-A-1:edid/theirs.bin"},
	} {
		if got := mergeEDIDOverride(c.cur, c.conn, c.file); got != c.want {
			t.Errorf("merge(%q, %s, %q) = %q, want %q", c.cur, c.conn, c.file, got, c.want)
		}
	}
}

// fakeKWinConnectorEnv fakes the sysfs port, the EDID parameter and firmware
// directory, the guard state, the root steps and KWin's stream.
type fakeKWinConnectorEnv struct {
	status, edidFile, param, fwDir string
	forced                         []string
	helper                         [][]string
	helperErr                      error
	applyEDID                      bool     // the kernel loads the EDID when forced on
	order                          []string // port writes and parameter writes, in turn
}

func newFakeKWinConnectorEnv(t *testing.T) *fakeKWinConnectorEnv {
	t.Helper()
	f := &fakeKWinConnectorEnv{applyEDID: true}
	f.status = fakeSysfsDRM(t, "disconnected", nil)
	f.edidFile = filepath.Join(filepath.Dir(f.status), "edid")
	dir := t.TempDir()
	f.param = filepath.Join(dir, "edid_firmware")
	os.WriteFile(f.param, []byte("\x00"), 0o644)
	f.fwDir = filepath.Join(dir, "fw")
	os.MkdirAll(f.fwDir, 0o755)

	prevParam, prevFw, prevForce, prevHelper, prevStream, prevGuard, prevRefused, prevWrite :=
		edidFirmwareParamPath, kwinEDIDFirmwareDir, forceConnectorPrivileged, runDisplayHelperFn, kwinStreamOutput, displayGuardDir, kwinConnectorRefused, edidParamDirectWrite
	prevRoot, prevPlugdev := kwinRootWithoutPrompt, kwinInPlugdev
	t.Cleanup(func() {
		edidFirmwareParamPath, kwinEDIDFirmwareDir, forceConnectorPrivileged, runDisplayHelperFn, kwinStreamOutput, displayGuardDir, kwinConnectorRefused, edidParamDirectWrite =
			prevParam, prevFw, prevForce, prevHelper, prevStream, prevGuard, prevRefused, prevWrite
		kwinRootWithoutPrompt, kwinInPlugdev = prevRoot, prevPlugdev
	})
	// A plugdev member: the udev rules open the port and the parameter.
	kwinRootWithoutPrompt, kwinInPlugdev = func() bool { return false }, func() bool { return true }
	// A write to a sysfs parameter replaces its whole value.
	edidParamDirectWrite = func(path string, value []byte) error {
		f.order = append(f.order, "param "+strings.Trim(string(value), "\x00"))
		return os.WriteFile(path, value, 0o644)
	}
	edidFirmwareParamPath, kwinEDIDFirmwareDir, kwinConnectorRefused = f.param, f.fwDir, nil
	guard := t.TempDir()
	displayGuardDir = func() (string, error) { return guard, nil }
	forceConnectorPrivileged = func(path, value string, _ bool) error {
		f.forced = append(f.forced, value)
		f.order = append(f.order, "port "+value)
		switch value {
		case "on":
			os.WriteFile(f.status, []byte("connected\n"), 0o644)
			if f.applyEDID {
				e, _ := os.ReadFile(filepath.Join(f.fwDir, "drawsplay-1920x1200.bin"))
				os.WriteFile(f.edidFile, e, 0o644)
			}
		case "detect":
			os.WriteFile(f.status, []byte("disconnected\n"), 0o644)
			os.WriteFile(f.edidFile, nil, 0o644)
		}
		return nil
	}
	runDisplayHelperFn = func(_ bool, args ...string) (string, error) {
		f.helper = append(f.helper, args)
		if f.helperErr != nil {
			return "", f.helperErr
		}
		if args[0] == "edid-install" {
			edid, _ := tabletEDID(1920, 1200)
			os.WriteFile(filepath.Join(f.fwDir, args[1]), edid, 0o644)
		}
		return "", nil
	}
	kwinStreamOutput = func(string, bool) (*kwinOutput, error) {
		return &kwinOutput{node: 42, closed: make(chan struct{})}, nil
	}
	return f
}

func (f *fakeKWinConnectorEnv) paramValue() string {
	b, _ := os.ReadFile(f.param)
	return string(b)
}

func TestKWinConnectorInstallsEDIDOnceAndCleansUp(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	ho, err := startKWinConnector(1920, 1200, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.helper) != 1 || f.helper[0][0] != "edid-install" || f.helper[0][1] != "drawsplay-1920x1200.bin" {
		t.Fatalf("helper runs = %v, want one edid-install", f.helper)
	}
	if ho.Name() != "HDMI-A-1" || !ho.CompositorSized() || ho.Target().HostResolution || ho.Target().Node != 42 {
		t.Fatalf("handle %s: the user must set its mode in the display settings", ho.Name())
	}
	if got := f.paramValue(); got != "HDMI-A-1:edid/drawsplay-1920x1200.bin" {
		t.Fatalf("edid_firmware = %q", got)
	}
	if err := ho.Remove(nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.paramValue(); got != "\x00" {
		t.Fatalf("edid_firmware after removal = %q, want emptied", got)
	}
	if !reflect.DeepEqual(f.forced, []string{"on", "detect"}) {
		t.Fatalf("port writes = %v", f.forced)
	}
	// Installed now: the next tablet session needs no root.
	f.helper = nil
	ho, err = startKWinConnector(1920, 1200, false)
	if err != nil {
		t.Fatal(err)
	}
	ho.Remove(nil, "")
	if len(f.helper) != 0 {
		t.Fatalf("second session ran the helper: %v", f.helper)
	}
}

func TestKWinConnectorUndoesWhenTheKernelIgnoresTheEDID(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	f.applyEDID = false
	if _, err := startKWinConnector(1920, 1200, false); err == nil {
		t.Fatal("started without the tablet's EDID on the port")
	}
	if !reflect.DeepEqual(f.forced, []string{"on", "detect"}) || f.paramValue() != "\x00" {
		t.Fatalf("not undone: writes %v, edid_firmware %q", f.forced, f.paramValue())
	}
}

func TestKWinConnectorDismissedPromptIsNotRepeated(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	f.helperErr = errPasswordPromptDismissed
	if _, err := startKWinConnector(1920, 1200, false); !errors.Is(err, errPasswordPromptDismissed) {
		t.Fatalf("err = %v", err)
	}
	if _, err := startKWinConnector(1920, 1200, false); !errors.Is(err, errKWinConnectorDeclined) {
		t.Fatalf("second try: err = %v, want the earlier refusal", err)
	}
	if len(f.helper) != 1 || len(f.forced) != 0 {
		t.Fatalf("helper %v, port writes %v", f.helper, f.forced)
	}
}

func TestKWinConnectorNeedsASparePort(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	os.WriteFile(f.status, []byte("connected\n"), 0o644) // a real monitor
	os.WriteFile(f.edidFile, []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0}, 0o644)
	if _, err := startKWinConnector(1920, 1200, false); !errors.Is(err, errNoSpareConnector) {
		t.Fatalf("err = %v", err)
	}
}

func TestReleaseKWinConnectorLeftovers(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	edid, _ := tabletEDID(1920, 1200)
	os.WriteFile(f.status, []byte("connected\n"), 0o644)
	os.WriteFile(f.edidFile, edid, 0o644)
	os.WriteFile(f.param, []byte("HDMI-A-1:edid/drawsplay-1920x1200.bin,DP-1:edid/mine.bin"), 0o644)
	if !isForcedX11Output("HDMI-1") {
		t.Fatal("a port showing the tablet EDID counts as forced")
	}
	releaseKWinConnectorLeftovers()
	if !reflect.DeepEqual(f.forced, []string{"detect"}) {
		t.Fatalf("port writes = %v", f.forced)
	}
	if got := f.paramValue(); got != "DP-1:edid/mine.bin" {
		t.Fatalf("edid_firmware = %q", got)
	}
}

// The override goes before the port is switched back to detection, so a
// monitor plugged in later reads its own EDID.
func TestKWinConnectorRemovesTheOverrideBeforeReleasingThePort(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	ho, err := startKWinConnector(1920, 1200, false)
	if err != nil {
		t.Fatal(err)
	}
	f.order = nil
	if err := ho.Remove(nil, ""); err != nil {
		t.Fatal(err)
	}
	if want := []string{"param ", "port detect"}; !reflect.DeepEqual(f.order, want) {
		t.Fatalf("release order = %q, want %q", f.order, want)
	}
}

// Without the plugdev group, root or passwordless sudo the port could not be
// released after the session: it is not used, and no password is asked for.
func TestKWinConnectorNeedsAWayToReleaseThePort(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	kwinInPlugdev = func() bool { return false }
	if _, err := startKWinConnector(1920, 1200, false); !errors.Is(err, errKWinConnectorNoAccess) {
		t.Fatalf("err = %v, want errKWinConnectorNoAccess", err)
	}
	if _, err := startKWinConnector(1920, 1200, false); !errors.Is(err, errKWinConnectorRefusedEarlier) {
		t.Fatalf("second try: err = %v, want the earlier refusal", err)
	}
	if len(f.helper) != 0 || len(f.forced) != 0 {
		t.Fatalf("helper %v, port writes %v", f.helper, f.forced)
	}
	// Passwordless sudo releases it too.
	kwinConnectorRefused = nil
	kwinRootWithoutPrompt = func() bool { return true }
	ho, err := startKWinConnector(1920, 1200, false)
	if err != nil {
		t.Fatal(err)
	}
	ho.Remove(nil, "")
}

// In the group but the rules not in effect (the status file stays root's):
// the port is ruled out after the EDID install, before it is forced on.
func TestKWinConnectorNeedsTheRulesInEffect(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	os.Chmod(f.status, 0o444)
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	if _, err := startKWinConnector(1920, 1200, false); !errors.Is(err, errKWinConnectorNoAccess) {
		t.Fatalf("err = %v, want errKWinConnectorNoAccess", err)
	}
	if len(f.forced) != 0 {
		t.Fatalf("port writes %v", f.forced)
	}
}

// A connector name another card shares is never used: the kernel override
// and KWin would reach that card's port too.
func TestKWinConnectorSkipsASharedConnectorName(t *testing.T) {
	f := newFakeKWinConnectorEnv(t)
	other := filepath.Join(sysfsDRMDir, "card2-HDMI-A-1")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "status"), []byte("connected\n"), 0o644)
	if _, err := startKWinConnector(1920, 1200, false); !errors.Is(err, errNoSpareConnector) {
		t.Fatalf("err = %v, want errNoSpareConnector", err)
	}
	if len(f.forced) != 0 || len(f.helper) != 0 {
		t.Fatalf("helper %v, port writes %v", f.helper, f.forced)
	}
}

// Setting the port up is guarded on its own: an interrupted KDE setup does
// not rule out the X11 forced-connector fallback.
func TestKWinConnectorHasItsOwnGuard(t *testing.T) {
	newFakeKWinConnectorEnv(t)
	displayGuardDenyUntil(guardMethodForcedConnector, "X11 failure", false)
	ho, err := startKWinConnector(1920, 1200, false)
	if err != nil {
		t.Fatalf("an X11 denial blocked the KDE port: %v", err)
	}
	ho.Remove(nil, "")
	displayGuardDenyUntil(guardMethodKWinConnector, "KDE failure", false)
	if _, err := startKWinConnector(1920, 1200, false); err == nil {
		t.Fatal("started despite the KDE denial")
	}
}
