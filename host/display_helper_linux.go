package main

import (
	"os"
	"syscall"
)

// displayHelperPath is where the Linux installer puts the root-side helper
// (packaging/linux/drawsplay-display-helper) and its polkit action
// com.drawsplay.display-helper. Through pkexec, the password prompt then
// names that action instead of asking to run a shell as root.
const displayHelperPath = "/usr/libexec/drawsplay/drawsplay-display-helper"

// displayHelper returns the helper's path when it is installed safely: a
// regular file owned by root and writable by nobody else. Otherwise callers
// fall back to the generic `pkexec sh -c` command.
func displayHelper() (string, bool) {
	return displayHelperAt(displayHelperPath)
}

func displayHelperAt(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 {
		return "", false
	}
	return path, true
}
