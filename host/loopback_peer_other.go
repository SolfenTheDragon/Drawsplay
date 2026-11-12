//go:build !linux && !windows

package main

import (
	"errors"
	"net"
)

// Platforms without a peer-ownership implementation must fail closed rather
// than treating every local process as the current user.
func loopbackPeerIsOtherUser(net.Conn) (bool, error) {
	return false, errors.New("peer ownership checks are unsupported on this platform")
}
