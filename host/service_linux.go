//go:build linux

package main

// Linux login autostart: an XDG autostart entry launches the host inside
// the desktop session, where display capture and input injection are available.

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	unitName      = "drawsplay-host.service"
	autostartName = "drawsplay-host.desktop"
)

func autostartPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "autostart")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, autostartName), nil
}

func systemctlUser(args ...string) ([]byte, error) {
	return exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
}

func legacyAutostartLinks() []string {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	paths := []string{filepath.Join(base, "systemd", "user", "graphical-session.target.wants", unitName)}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		paths = append(paths, filepath.Join(runtimeDir, "systemd", "user", "graphical-session.target.wants", unitName))
	}
	return paths
}

func legacyAutostartEnabled() bool {
	for _, path := range legacyAutostartLinks() {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}

func disableLegacyAutostart() error {
	if !legacyAutostartEnabled() {
		return nil
	}
	if out, err := systemctlUser("disable", unitName); err != nil {
		return fmt.Errorf("disabling legacy systemd autostart: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func desktopExecArg(s string) string {
	// Escape Exec-field metacharacters, then escape those backslashes for
	// the enclosing Desktop Entry key file.
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s)
	s = strings.ReplaceAll(s, `\`, `\\`)
	return `"` + s + `"`
}

func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := autostartPath()
	if err != nil {
		return err
	}
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=DrawSplay host
Exec=%s -background
Terminal=false
NoDisplay=true
X-GNOME-Autostart-enabled=true
`, desktopExecArg(exe))
	if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
		return fmt.Errorf("writing autostart entry: %w", err)
	}
	return disableLegacyAutostart()
}

func uninstallAutostart() error {
	// Disable future logins without stopping the currently running host.
	path, err := autostartPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing autostart entry: %w", err)
	}
	return disableLegacyAutostart()
}

func autostartEnabled() (bool, error) {
	path, err := autostartPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	// Preserve an existing opt-in during upgrade: the old unit's
	// graphical-session.target dependency does not start in some X11 sessions.
	if !legacyAutostartEnabled() {
		return false, nil
	}
	if err := installAutostart(); err != nil {
		return false, fmt.Errorf("migrating legacy autostart: %w", err)
	}
	return true, nil
}

func prepareBackground() {
	dir, err := configDir()
	if err != nil {
		log.Printf("-background: cannot locate the config directory: %v", err)
		return
	}
	path := filepath.Join(dir, "host.log")
	_ = os.Remove(path + ".old")
	_ = os.Rename(path, path+".old")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		log.Printf("-background: cannot open log file: %v", err)
		return
	}
	log.SetOutput(io.MultiWriter(f, &debugLogWriter{}))
}
