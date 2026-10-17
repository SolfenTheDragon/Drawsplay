//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// Installer, tray-by-default and console attach are Windows-only; other
// platforms keep their existing behavior.

const defaultBackgroundMode = false

type installerOptions struct {
	Install           bool
	Uninstall         bool
	Quiet             bool
	KeepSettings      bool
	RemoveShared      bool
	DesktopShortcut   bool
	Autostart         bool // start at sign-in without asking
	NoAutostart       bool // do not start at sign-in, without asking
	SkipVDD           bool
	InstallElevated   string
	WinUSBBind        bool // internal: elevated WinUSB driver setup (scheduled task / UAC)
	UninstallElevated string
	NoArgs            bool
}

func runInstallerMode(o installerOptions) (bool, int) {
	if o.Install || o.Uninstall || o.InstallElevated != "" || o.UninstallElevated != "" || o.WinUSBBind {
		fmt.Fprintln(os.Stderr, "-install/-uninstall are Windows-only; use scripts/install-host-linux.sh on Linux")
		return true, 2
	}
	return false, 0
}

func attachParentConsole() {}

func handOffToRunningInstance(bool) bool { return false }

// hideChildWindow is a Windows concern (console tools spawned by the
// GUI-subsystem host); a no-op elsewhere.
func hideChildWindow(*exec.Cmd) {}
