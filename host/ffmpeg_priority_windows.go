//go:build windows

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// applyCaptureProcessPriority elevates ffmpeg to reduce scheduling jitter.
// Android client visibility can lower or restore it for background keep-alive.
func applyCaptureProcessPriority(cmd *exec.Cmd) {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.CreationFlags |= windows.HIGH_PRIORITY_CLASS
	cmd.SysProcAttr = attr
	hideChildWindow(cmd)
}

func setCaptureProcessPriority(cmd *exec.Cmd, foreground bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	class := uint32(windows.BELOW_NORMAL_PRIORITY_CLASS)
	if foreground {
		class = windows.HIGH_PRIORITY_CLASS
	}
	return windows.SetPriorityClass(process, class)
}

// applyCaptureProcessPriorityPostStart is a no-op on Windows: priority is
// already applied above, before Start(), via the process creation flags.
func applyCaptureProcessPriorityPostStart(cmd *exec.Cmd) {}
