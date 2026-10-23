//go:build !linux && !windows

package wifilan

import (
	"errors"
	"os"
)

var errTrustPlatform = errors.New("persistent Wi-Fi trust supports Linux and Windows only")

func secureStorePath(string, bool) error       { return errTrustPlatform }
func lockTrustFile(*os.File) error             { return errTrustPlatform }
func protectTrustKey([]byte) ([]byte, error)   { return nil, errTrustPlatform }
func unprotectTrustKey([]byte) ([]byte, error) { return nil, errTrustPlatform }
func syncTrustDirectory(*os.Root) error        { return errTrustPlatform }
