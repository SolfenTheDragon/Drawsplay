//go:build windows

package main

func platformDependencyWarnings() map[string]string { return nil }

func x11SessionActive() bool { return false }

func waylandSessionActive() bool { return false }

func waylandVirtualDisplayName() string { return "" }

func x11VirtualDisplayReady() bool { return false }

func refreshX11VkmsWarning() {}

func ensureX11VirtualDisplayAsync(reason string, mayPrompt bool) {}
