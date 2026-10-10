//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallAutostartLeavesRunningServiceAlone(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	unit := filepath.Join(configDir, "systemd", "user", unitName)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	callsPath := filepath.Join(t.TempDir(), "systemctl-calls")
	fakeSystemctl := filepath.Join(binDir, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SYSTEMCTL_CALLS\"\n"
	if err := os.WriteFile(fakeSystemctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("SYSTEMCTL_CALLS", callsPath)

	if err := uninstallAutostart(); err != nil {
		t.Fatalf("uninstallAutostart() error = %v", err)
	}

	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(calls)), "\n")
	want := []string{"--user disable " + unitName}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("systemctl calls = %q, want %q", got, want)
	}
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("service unit should remain available for manual starts: %v", err)
	}
}
