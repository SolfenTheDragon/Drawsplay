//go:build !windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func isNetworkExecutablePath(string) bool { return false }

func validateLocalExecutable(path string, info fs.FileInfo) error {
	if info.Mode().Perm()&0o111 == 0 {
		return errors.New("file is not executable")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (int(st.Uid) != os.Getuid() && st.Uid != 0) {
		return errors.New("file must be owned by this user or root")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("file must not be writable by group or others")
	}
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect executable path: %w", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		linkStat, ok := linkInfo.Sys().(*syscall.Stat_t)
		if !ok || (int(linkStat.Uid) != os.Getuid() && linkStat.Uid != 0) {
			return errors.New("executable symlink must be owned by this user or root")
		}
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	for _, directory := range []string{filepath.Dir(path), filepath.Dir(resolvedPath)} {
		for current := directory; ; current = filepath.Dir(current) {
			parent, err := os.Lstat(current)
			if err != nil {
				return fmt.Errorf("inspect executable directory: %w", err)
			}
			parentStat, ok := parent.Sys().(*syscall.Stat_t)
			if !ok || (int(parentStat.Uid) != os.Getuid() && parentStat.Uid != 0) {
				return errors.New("executable directory must be owned by this user or root")
			}
			if parent.Mode()&os.ModeSymlink == 0 {
				if !parent.IsDir() {
					return errors.New("executable path contains a non-directory component")
				}
				if parent.Mode().Perm()&0o022 != 0 &&
					!(parentStat.Uid == 0 && parent.Mode()&os.ModeSticky != 0) {
					return errors.New("executable directory must not be writable by group or others")
				}
			}
			if current == filepath.Dir(current) {
				break
			}
		}
	}
	return nil
}
