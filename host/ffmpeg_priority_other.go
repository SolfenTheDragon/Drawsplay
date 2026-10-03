//go:build !windows && !linux

package main

import "os/exec"

func applyCaptureProcessPriority(cmd *exec.Cmd) {}

// applyCaptureProcessPriorityPostStart is a no-op here; see
// ffmpeg_priority_linux.go for the Linux implementation, which needs the
// process's pid and so must run after Start() rather than before it.
func applyCaptureProcessPriorityPostStart(cmd *exec.Cmd) {}
