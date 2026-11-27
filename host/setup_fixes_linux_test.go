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

func readPackaged(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "packaging", "linux", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The host runs its embedded copy when the installed helper is older, so the
// two must not drift.
func TestEmbeddedDisplayHelperMatchesPackaged(t *testing.T) {
	if embeddedDisplayHelper != readPackaged(t, "drawsplay-display-helper") {
		t.Fatal("host/linuxhelper/drawsplay-display-helper differs from packaging/linux/drawsplay-display-helper; copy the packaged file over it")
	}
}

func TestDisplayHelperCarriesPackagedUdevRules(t *testing.T) {
	for _, name := range []string{"99-drawsplay-uinput.rules", "70-drawsplay-aoa.rules", "71-drawsplay-edid-override.rules"} {
		heredoc := "  cat <<'RULE'\n" + readPackaged(t, name) + "RULE\n"
		if !strings.Contains(embeddedDisplayHelper, heredoc) {
			t.Errorf("drawsplay-display-helper does not carry %s verbatim", name)
		}
	}
}

func TestSetupFixesRunHelperActions(t *testing.T) {
	prevHelper, prevPreflight := runDisplayHelperFn, runPreflightNow
	t.Cleanup(func() { runDisplayHelperFn, runPreflightNow = prevHelper, prevPreflight })
	runPreflightNow = func(settings) {}
	var got []string
	runDisplayHelperFn = func(_ bool, args ...string) (string, error) {
		got = args
		return "", nil
	}
	cfg := defaultSettings()
	for action, want := range map[string][]string{
		setupFixUSBRule:        {"udev-rule", "aoa"},
		setupFixUinputRule:     {"udev-rule", "uinput"},
		setupFixFFmpeg:         {"packages", "ffmpeg"},
		setupFixWaylandCapture: {"packages", "wayland-capture"},
	} {
		got = nil
		res, err := runSetupFix(action, cfg)
		if err != nil || res.Message == "" {
			t.Fatalf("%s: res=%+v err=%v", action, res, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s ran helper %v, want %v", action, got, want)
		}
		// The helper must know the action it is asked for.
		if !strings.Contains(embeddedDisplayHelper, "\n  "+want[0]+")") ||
			!strings.Contains(embeddedDisplayHelper, "\n      "+want[1]+")") {
			t.Fatalf("display helper lacks %v", want)
		}
	}

	runDisplayHelperFn = func(bool, ...string) (string, error) { return "", errPasswordPromptDismissed }
	if _, err := runSetupFix(setupFixFFmpeg, cfg); !errors.Is(err, errPasswordPromptDismissed) {
		t.Fatalf("dismissed prompt: err = %v", err)
	}
	if _, err := runSetupFix("no-such-fix", cfg); !errors.Is(err, errUnknownSetupFix) {
		t.Fatalf("unknown fix: err = %v", err)
	}
}

func TestSetupChecklistOffersFixesOnLinux(t *testing.T) {
	if fix, _ := adbSetupFix("linux", "device lacks USB permissions"); fix != setupFixUSBRule {
		t.Fatalf("ADB permissions fix = %q", fix)
	}
	if fix, _ := adbSetupFix("linux", "device not authorized"); fix != setupFixRestartADB {
		t.Fatalf("ADB authorization fix = %q", fix)
	}
	if item := aoaSetupItem("linux", aoaStatusInfo{State: "error", Detail: "permission denied"}); item.Fix != setupFixUSBRule || item.FixLabel == "" {
		t.Fatalf("AOA error item = %+v", item)
	}
	if item := ffmpegSetupItem("linux", []string{"ffmpeg is unavailable"}); item.OK || item.Fix != setupFixFFmpeg {
		t.Fatalf("ffmpeg item = %+v", item)
	}
}

func TestX11DisplayFix(t *testing.T) {
	prevBlocked, prevSpare := vkmsBlockedFn, spareHDMIConnectorFn
	t.Cleanup(func() { vkmsBlockedFn, spareHDMIConnectorFn = prevBlocked, prevSpare })
	if vkmsModuleLoaded() {
		t.Skip("vkms is loaded on this machine")
	}
	vkmsBlockedFn = func() bool { return false }
	if fix, _, _ := x11DisplayFix(); fix != setupFixVirtualDisplay {
		t.Fatalf("unloaded, not blocked: fix = %q", fix)
	}
	vkmsBlockedFn = func() bool { return true }
	spareHDMIConnectorFn = func() bool { return false }
	if fix, _, _ := x11DisplayFix(); fix != "" {
		t.Fatalf("blocked without a spare connector: fix = %q", fix)
	}
}
