package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateConfiguredExecutableAllowsDefaultsAndLocalFile(t *testing.T) {
	for _, name := range []string{"adb", "adb.exe", "ffmpeg", "ffmpeg.exe"} {
		tool := "adb"
		if name == "ffmpeg" || name == "ffmpeg.exe" {
			tool = "ffmpeg"
		}
		if err := validateConfiguredExecutable(name, tool); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}

	name := "adb"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateConfiguredExecutable(path, "adb"); err != nil {
		t.Fatalf("local executable rejected: %v", err)
	}
}

func TestValidateConfiguredExecutableRejectsUnsafeForms(t *testing.T) {
	for _, value := range []string{
		"",
		"relative/adb.exe",
		`\\server\share\adb.exe`,
		"https://example.test/adb.exe",
		"file:///C:/tools/adb.exe",
	} {
		if err := validateConfiguredExecutable(value, "adb"); err == nil {
			t.Errorf("%q unexpectedly accepted", value)
		}
	}
}

func TestSettingsRejectsUnsafePreferredSerial(t *testing.T) {
	s := defaultSettings()
	s.PreferredADBSerial = "serial with spaces"
	if err := s.validate(); err == nil {
		t.Error("serial containing whitespace unexpectedly accepted")
	}
}

func TestSettingsVideoScalingValidation(t *testing.T) {
	if got := defaultSettings().VideoScaling; got != videoScalingFill {
		t.Fatalf("default video scaling = %q, want %q", got, videoScalingFill)
	}
	for _, mode := range []string{videoScalingFill, videoScalingFit, videoScalingOriginal} {
		s := defaultSettings()
		s.VideoScaling = mode
		if err := s.validate(); err != nil {
			t.Errorf("video scaling %q rejected: %v", mode, err)
		}
	}
	for _, mode := range []string{"", "stretch", "FIT"} {
		s := defaultSettings()
		s.VideoScaling = mode
		if err := s.validate(); err == nil {
			t.Errorf("video scaling %q unexpectedly accepted", mode)
		}
	}
}

// TestSettingsStoreUpdateResolutionPersists guards the fix for Linux's
// GNOME-managed resolution snapping back to the originally configured size
// on every session restart: the extended displays' watchers
// (mutter_extend_linux.go, x11_extend_linux.go) call updateResolution when
// they detect a display-settings change, and that new width/height must
// actually be written to disk (not just held in the in-memory snapshot), or
// the next session would set the stale old default again.
func TestSettingsStoreUpdateResolutionPersists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	st := newSettingsStore(defaultSettings())
	if err := st.updateResolution(2560, 1440); err != nil {
		t.Fatalf("updateResolution: %v", err)
	}
	if got := st.get(); got.Width != 2560 || got.Height != 1440 {
		t.Fatalf("in-memory settings = %dx%d, want 2560x1440", got.Width, got.Height)
	}

	// A fresh store loading from disk (as a brand-new process would) must
	// see the same resolution -- proving it was actually persisted, not
	// just mutated in memory.
	reloaded := loadSettings()
	if reloaded.Width != 2560 || reloaded.Height != 1440 {
		t.Fatalf("reloaded settings = %dx%d, want 2560x1440", reloaded.Width, reloaded.Height)
	}
}

func TestSettingsRequirePreferredSerial(t *testing.T) {
	s := defaultSettings()
	s.RequirePreferredADB = true
	s.PreferredADBSerial = ""
	if err := s.validate(); err == nil {
		t.Error("strict device trust accepted with empty preferred serial")
	}
	s.PreferredADBSerial = "tablet-123"
	if err := s.validate(); err != nil {
		t.Fatalf("valid strict serial rejected: %v", err)
	}
}
