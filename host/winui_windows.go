//go:build windows

package main

// Windows desktop-app plumbing. Release builds use the GUI subsystem
// (-ldflags -H=windowsgui) so launching the host never opens a console: by
// default it runs in the background with a tray icon, and the installer and
// uninstaller talk to the user through message boxes. When started from a
// terminal, the host attaches to that terminal's console for its output.

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// defaultBackgroundMode makes tray/background the default on Windows; pass
// -background=false for a console-logging foreground run.
const defaultBackgroundMode = true

const (
	mbOK              = 0x00000000
	mbYesNo           = 0x00000004
	mbIconError       = 0x00000010
	mbIconQuestion    = 0x00000020
	mbIconWarning     = 0x00000030
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	idYes             = 6

	attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS ((DWORD)-1)
)

var (
	user32WinUI       = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW   = user32WinUI.NewProc("MessageBoxW")
	kernel32WinUI     = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole = kernel32WinUI.NewProc("AttachConsole")
)

func messageBox(title, text string, flags uint32) int {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0
	}
	m, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return 0
	}
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), uintptr(flags|mbSetForeground))
	return int(ret)
}

func askYesNo(title, text string) bool {
	return messageBox(title, text, mbYesNo|mbIconQuestion) == idYes
}

func showInfo(title, text string) {
	messageBox(title, text, mbOK|mbIconInformation)
}

func showWarning(title, text string) {
	messageBox(title, text, mbOK|mbIconWarning)
}

func showError(title, text string) {
	messageBox(title, text, mbOK|mbIconError)
}

// attachParentConsole routes output to the launching terminal when a
// GUI-subsystem build is started from one (it has no console of its own).
// It is a no-op for console builds or when output is redirected.
func attachParentConsole() {
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		return
	}
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	os.Stdout = f
	os.Stderr = f
	log.SetOutput(f)
}

// hideChildWindow keeps console tools (powershell, pnputil, schtasks) from
// flashing a window when spawned by the console-less host.
func hideChildWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func runHiddenCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	hideChildWindow(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func system32Path(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", name)
}

func schtasksPath() string { return system32Path("schtasks.exe") }

func windowsPowerShellPath() string {
	return system32Path(filepath.Join("WindowsPowerShell", "v1.0", "powershell.exe"))
}
