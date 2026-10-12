//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fakeSystemctl(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	callsPath := filepath.Join(t.TempDir(), "systemctl-calls")
	fake := filepath.Join(binDir, "systemctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SYSTEMCTL_CALLS\"\nif [ \"$2\" = disable ] && [ -n \"$SYSTEMD_LEGACY_LINK\" ]; then /bin/rm -f \"$SYSTEMD_LEGACY_LINK\"; fi\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("SYSTEMCTL_CALLS", callsPath)
	return callsPath
}

func enableLegacyAutostartLink(t *testing.T, configDir string) {
	t.Helper()
	path := filepath.Join(configDir, "systemd", "user", "graphical-session.target.wants", unitName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(configDir, "systemd", "user", unitName), path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYSTEMD_LEGACY_LINK", path)
}

func TestInstallAutostartUsesDesktopSessionAndMigratesSystemd(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	enableLegacyAutostartLink(t, configDir)
	validator, _ := exec.LookPath("desktop-file-validate")
	callsPath := fakeSystemctl(t)

	if err := installAutostart(); err != nil {
		t.Fatalf("installAutostart() error = %v", err)
	}
	path := filepath.Join(configDir, "autostart", autostartName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := string(data)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry, "[Desktop Entry]\n") ||
		!strings.Contains(entry, "Exec="+desktopExecArg(exe)+" -background\n") ||
		!strings.Contains(entry, "X-GNOME-Autostart-enabled=true\n") ||
		!strings.Contains(entry, "Terminal=false\n") {
		t.Fatalf("unexpected autostart entry: %q", entry)
	}
	if validator != "" {
		if out, err := exec.Command(validator, path).CombinedOutput(); err != nil {
			t.Fatalf("desktop-file-validate: %v (%s)", err, out)
		}
	}
	if enabled, err := autostartEnabled(); err != nil || !enabled {
		t.Fatalf("autostartEnabled() = %v, %v; want true", enabled, err)
	}

	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "--user disable " + unitName + "\n"
	if string(calls) != want {
		t.Fatalf("systemctl calls = %q, want %q", calls, want)
	}
}

func TestUninstallAutostartLeavesRunningServiceAlone(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	enableLegacyAutostartLink(t, configDir)
	path := filepath.Join(configDir, "autostart", autostartName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Desktop Entry]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	callsPath := fakeSystemctl(t)

	if err := uninstallAutostart(); err != nil {
		t.Fatalf("uninstallAutostart() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("autostart entry should be removed: %v", err)
	}
	if enabled, err := autostartEnabled(); err != nil || enabled {
		t.Fatalf("autostartEnabled() = %v, %v; want false", enabled, err)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "--user disable " + unitName + "\n"
	if string(calls) != want {
		t.Fatalf("systemctl calls = %q, want %q", calls, want)
	}
}

func TestAutostartStatusMigratesExistingEnabledService(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	enableLegacyAutostartLink(t, configDir)
	callsPath := fakeSystemctl(t)

	if enabled, err := autostartEnabled(); err != nil || !enabled {
		t.Fatalf("autostartEnabled() = %v, %v; want true", enabled, err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "autostart", autostartName)); err != nil {
		t.Fatalf("legacy startup preference was not migrated: %v", err)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "--user disable " + unitName + "\n"
	if string(calls) != want {
		t.Fatalf("systemctl calls = %q, want %q", calls, want)
	}
}

func TestInstallAutostartDoesNotRequireSystemd(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("PATH", t.TempDir())

	if err := installAutostart(); err != nil {
		t.Fatalf("installAutostart() without systemd: %v", err)
	}
	if enabled, err := autostartEnabled(); err != nil || !enabled {
		t.Fatalf("autostartEnabled() = %v, %v; want true", enabled, err)
	}
}

func TestDesktopExecArgEscapesPathCharacters(t *testing.T) {
	got := desktopExecArg(`/opt/my "apps"/$HOME\drawsplay`)
	for _, escaped := range []string{`\\"`, `\\$`, `\\\\drawsplay`} {
		if !strings.Contains(got, escaped) {
			t.Fatalf("desktopExecArg() = %q, missing %q", got, escaped)
		}
	}
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Fatalf("desktopExecArg() is not quoted: %q", got)
	}
}
