//go:build linux

package main

import "errors"

// Never touch the real DRM connectors, load vkms, or run sudo/pkexec from
// tests: an unstubbed run once forced the developer's HDMI port on, and loading
// vkms has crashed X (vkms_prepare_fb on Linux 7.0 OEM). Tests that exercise
// the spare-connector path point sysfsDRMDir at a temporary tree themselves.
func init() {
	sysfsDRMDir = "/nonexistent/drawsplay-test-drm"
	vkmsLoadPrivileged = func(bool) error { return errors.New("loading vkms is disabled in tests") }
	forceConnectorPrivileged = func(string, string, bool) error {
		return errors.New("forcing connectors is disabled in tests")
	}
}
