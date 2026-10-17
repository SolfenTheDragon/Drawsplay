//go:build !windows

package main

import "os/exec"

func setCaptureProcessPriority(cmd *exec.Cmd, foreground bool) error { return nil }
