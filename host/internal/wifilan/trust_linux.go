package wifilan

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func secureStorePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory ||
		(!directory && !info.Mode().IsRegular()) || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("Wi-Fi trust path %q must be private (directory 0700, file 0600), without symlinks", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("Wi-Fi trust path must be owned by the current user")
	}
	return nil
}

func lockTrustFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func protectTrustKey(data []byte) ([]byte, error)   { return append([]byte{}, data...), nil }
func unprotectTrustKey(data []byte) ([]byte, error) { return append([]byte{}, data...), nil }

func syncTrustDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
