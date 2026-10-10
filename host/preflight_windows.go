//go:build windows

package main

func platformDependencyWarnings() map[string]string { return nil }

func x11SessionActive() bool { return false }
