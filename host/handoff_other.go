//go:build !windows && !linux

package main

func handOffToRunningInstance(bool) bool { return false }
