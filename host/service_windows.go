//go:build windows

package main

// Windows login autostart: an HKCU Run entry (no elevation required)
// launching `host.exe -background`.
//
// host.exe is a console-subsystem binary, so anything launched from the Run
// key would open a console window at every login. -background works around
// it: the first process immediately respawns itself with CREATE_NO_WINDOW
// (the login console flashes for well under a second) and the hidden child
// carries on with logs redirected to %APPDATA%\DrawSplay\host.log
// (previous log kept as host.log.old).

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "DrawSplay"
	// bgChildEnv marks the hidden respawned child so it does not respawn
	// again.
	bgChildEnv = "DRAWSPLAY_BACKGROUND_CHILD"
)

func installAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening Run key: %w", err)
	}
	defer key.Close()
	cmd := fmt.Sprintf(`"%s" -background`, exe)
	if err := key.SetStringValue(runValueName, cmd); err != nil {
		return fmt.Errorf("writing Run entry: %w", err)
	}
	return nil
}

func uninstallAutostart() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening Run key: %w", err)
	}
	defer key.Close()
	if err := key.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("removing Run entry: %w", err)
	}
	return nil
}

func autostartEnabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, fmt.Errorf("opening Run key: %w", err)
	}
	defer key.Close()
	if _, _, err := key.GetStringValue(runValueName); err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// prepareBackground implements -background: the first process respawns
// itself hidden and exits; the hidden child redirects logging to a file
// (it has no console to log to).
func prepareBackground() {
	if os.Getenv(bgChildEnv) == "" {
		respawnHidden()
		// Unreachable on success; respawnHidden exits the process.
		return
	}
	redirectLogsToFile()
}

func respawnHidden() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("-background: cannot resolve executable: %v", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), bgChildEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		log.Fatalf("-background: relaunch failed: %v", err)
	}
	os.Exit(0)
}

func redirectLogsToFile() {
	dir, err := configDir()
	if err != nil {
		return // no config dir: keep default (invisible) log output
	}
	path := filepath.Join(dir, "host.log")
	// Keep one previous session's log for post-mortems.
	_ = os.Remove(path + ".old")
	_ = os.Rename(path, path+".old")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	// Keep UI debug-log capture active even when background mode redirects
	// runtime logs to file.
	log.SetOutput(io.MultiWriter(f, &debugLogWriter{}))
}
