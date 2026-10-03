//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// captureProcessNiceDelta is how much the capture/encode subprocess's
// scheduling priority is raised. Linux niceness has no exec-time flag
// equivalent to Windows' HIGH_PRIORITY_CLASS, so this is applied via
// setpriority(2) once the process exists (see applyCaptureProcessPriorityPostStart).
//
// -10 asks the scheduler to favor the pipeline over default-niceness
// desktop work (compositor, browser, etc.) enough to cut scheduling jitter
// on a busy laptop, without the starvation risk of the most negative values.
// It is a best-effort request: unprivileged processes are usually capped by
// RLIMIT_NICE (commonly 0 on stock distros, i.e. no negative niceness), so
// this silently does nothing beyond default niceness unless the host runs
// with elevated limits or as root. That mirrors the existing pattern in this
// file's siblings, which also degrade gracefully when a knob is unavailable.
const captureProcessNiceDelta = -10

// applyCaptureProcessPriority is a no-op on Linux: unlike Windows'
// CreationFlags, there is no priority to set before the process exists, so
// the real work happens post-start (see applyCaptureProcessPriorityPostStart).
func applyCaptureProcessPriority(cmd *exec.Cmd) {}

// applyCaptureProcessPriorityPostStart best-effort renices the just-started
// capture/encode subprocess (gst-launch-1.0 or ffmpeg) to reduce the chance
// that compositor/browser/CPU-governor scheduling jitter delays a video
// frame's capture or encode, which shows up to the user as stutter or added
// glass-to-glass latency. Errors are ignored: a permission-denied renice
// still leaves the pipeline running at default niceness.
func applyCaptureProcessPriorityPostStart(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, captureProcessNiceDelta)
}
