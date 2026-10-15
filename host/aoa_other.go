//go:build !linux && !windows

package main

import "errors"

func aoaScanDevices() ([]aoaCandidate, error) {
	return nil, errors.New("USB accessory transport is not supported on this platform")
}
