//go:build windows

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// applyCaptureProcessPriority elevates ffmpeg to reduce scheduling jitter.
func applyCaptureProcessPriority(cmd *exec.Cmd) {
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.CreationFlags |= windows.HIGH_PRIORITY_CLASS
	cmd.SysProcAttr = attr
}

// applyCaptureProcessPriorityPostStart is a no-op on Windows: priority is
// already applied above, before Start(), via the process creation flags.
func applyCaptureProcessPriorityPostStart(cmd *exec.Cmd) {}
