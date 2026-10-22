package wifilan

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func restrictLANSocket(fd uintptr, iface net.Interface) error {
	if err := unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface.Name); err != nil {
		return fmt.Errorf("bind Wi-Fi socket to %s (no default-route fallback): %w", iface.Name, err)
	}
	if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_DONTROUTE, 1); err != nil {
		return fmt.Errorf("disable Wi-Fi gateway routing: %w", err)
	}
	return nil
}
