//go:build linux

package main

import "errors"

// Never touch the real DRM connectors (or sudo/pkexec) from tests: an
// unstubbed run once forced the developer's HDMI port on. Tests that exercise
// the spare-connector path point sysfsDRMDir at a temporary tree themselves.
func init() {
	sysfsDRMDir = "/nonexistent/drawsplay-test-drm"
	forceConnectorPrivileged = func(string, string, bool) error {
		return errors.New("forcing connectors is disabled in tests")
	}
}
