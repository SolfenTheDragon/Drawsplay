//go:build !linux

package wifilan

import (
	"errors"
	"net"
)

func restrictLANSocket(_ uintptr, _ net.Interface) error {
	return errors.New("strict selected-LAN socket routing is not implemented on this host OS; Wi-Fi dialing is disabled rather than using a default route")
}
