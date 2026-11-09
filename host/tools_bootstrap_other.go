//go:build !windows

package main

import "fmt"

func installBundledTool(tool string) (string, error) {
	return "", fmt.Errorf("auto-install of bundled %s is only implemented on Windows", tool)
}

func verifyBundledToolCandidate(string, string) bool { return true }
