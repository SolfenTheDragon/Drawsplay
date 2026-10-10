package main

// Login autostart management (the "run as a service" model).
//
// The host must run inside the user's desktop session: on Windows, DXGI
// desktop duplication and synthetic input injection are unavailable to
// session-0 services, and on Linux the Mutter ScreenCast + user D-Bus APIs
// require the user session. A native system service therefore cannot do
// this app's job; instead the host registers itself to start at login:
//
//	Windows: HKCU\...\CurrentVersion\Run entry launching `host -background`
//	         (-background respawns the process without a console window and
//	         redirects logs to %APPDATA%\DrawSplay\host.log).
//	Linux:   systemd user unit (~/.config/systemd/user/drawsplay-host.service),
//	         enabled via `systemctl --user`; journald captures the logs.
//
// Platform implementations live in service_windows.go / service_linux.go:
//
//	installAutostart() error
//	uninstallAutostart() error
//	autostartEnabled() (bool, error)
//	prepareBackground()  — post-flag-parse hook for -background mode
//
// Both the `-service` CLI subcommand and the web UI toggle call these.

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func autostartImplementation() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows per-user startup entry (HKCU Run key)"
	case "linux":
		switch strings.ToLower(strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE"))) {
		case "x11":
			return "Linux X11 session: systemd user service"
		case "wayland":
			return "Linux Wayland session: systemd user service"
		default:
			return "Linux desktop session: systemd user service"
		}
	default:
		return runtime.GOOS + ": login service implementation"
	}
}

// runServiceCommand executes `-service <cmd>` and returns the process exit
// code.
func runServiceCommand(cmd string) int {
	switch cmd {
	case "install":
		if err := installAutostart(); err != nil {
			fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
			return 1
		}
		fmt.Println("Autostart installed: the host will start at login.")
		fmt.Println("Start it now by running the host normally, or log out and in.")
		return 0
	case "uninstall":
		if err := uninstallAutostart(); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall failed: %v\n", err)
			return 1
		}
		fmt.Println("Autostart removed.")
		return 0
	case "status":
		enabled, err := autostartEnabled()
		if err != nil {
			fmt.Fprintf(os.Stderr, "status check failed: %v\n", err)
			return 1
		}
		if enabled {
			fmt.Println("Autostart is installed.")
		} else {
			fmt.Println("Autostart is not installed.")
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown -service command %q (want install, uninstall or status)\n", cmd)
		return 2
	}
}
