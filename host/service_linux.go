//go:build linux

package main

// Linux login autostart: a systemd user unit. The host needs the user
// session (Mutter ScreenCast D-Bus, user bus, Wayland), so a user unit —
// not a system service — is the correct integration. journald captures
// stdout/stderr, so -background needs no log redirection here.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const unitName = "drawsplay-host.service"

func unitPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, unitName), nil
}

func systemctlUser(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %v: %v (%s)", args, err, string(out))
	}
	return nil
}

func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	path, err := unitPath()
	if err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=DrawSplay host (tablet secondary display)
After=graphical-session.target

[Service]
ExecStart=%s -background
Restart=on-failure
RestartSec=3
# Raises the service's RLIMIT_NICE ceiling so the capture/encode subprocess
# (spawned by this service and inheriting its limits) can actually renice
# itself higher via setpriority(2); see applyCaptureProcessPriorityPostStart
# in ffmpeg_priority_linux.go. Stock distro defaults cap unprivileged
# processes at niceness 0, silently turning that renice into a no-op.
# LimitNICE=40 maps to the full -20..19 niceness range (systemd encodes it as
# 20 - niceness); this does not itself change any process's niceness.
LimitNICE=40

[Install]
WantedBy=graphical-session.target
`, exe)
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("writing unit: %w", err)
	}
	if err := systemctlUser("daemon-reload"); err != nil {
		return err
	}
	return systemctlUser("enable", unitName)
}

func uninstallAutostart() error {
	// Best-effort disable/stop before removing the unit file.
	_ = systemctlUser("disable", "--now", unitName)
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing unit: %w", err)
	}
	return systemctlUser("daemon-reload")
}

func autostartEnabled() (bool, error) {
	path, err := unitPath()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// prepareBackground is a no-op on Linux: systemd runs the process without a
// terminal and journald captures its logs.
func prepareBackground() {}
