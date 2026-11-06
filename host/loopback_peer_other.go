//go:build !linux && !windows

package main

import "net"

// loopbackPeerIsOtherUser has no implementation here; connections are accepted.
func loopbackPeerIsOtherUser(net.Conn) (bool, error) { return false, nil }
