//go:build !windows

package main

import (
	"fmt"
	"io/fs"
)

func isNetworkExecutablePath(string) bool { return false }

func validateLocalExecutable(_ string, info fs.FileInfo) error {
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("file is not executable")
	}
	return nil
}
