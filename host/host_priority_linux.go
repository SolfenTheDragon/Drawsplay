//go:build linux

package main

import "syscall"

// hostProcessNice mirrors captureProcessNiceDelta for the host itself, so the
// goroutines draining the encoder are not starved by desktop load. Unprivileged
// processes are capped by RLIMIT_NICE (usually 0), where this is a no-op.
const hostProcessNice = -5

func raiseHostProcessPriority() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, hostProcessNice)
}
