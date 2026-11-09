//go:build !windows

package main

import (
	"errors"
	"io/fs"
)

func isNetworkExecutablePath(string) bool { return false }

func validateLocalExecutable(_ string, info fs.FileInfo) error {
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("file is not executable")
	}
	return nil
}
