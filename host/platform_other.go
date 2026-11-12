//go:build !linux && !windows

package main

// The host supports Linux and Windows. These fallbacks keep it compiling
// elsewhere: it starts and serves the settings page, but cannot capture,
// inject input or start at login.

import "errors"

var errPlatformUnsupported = errors.New("not supported on this operating system")

func newInjector() injector { return noopInjector{} }

func newVirtualDisplay(opts videoOptions) (virtualDisplay, error) {
	return nil, errPlatformUnsupported
}

func buildCapturePipelines(target captureTarget, opts videoOptions) ([]pipelineSpec, error) {
	return nil, errPlatformUnsupported
}

func prepareBackground() {}

func installAutostart() error { return errPlatformUnsupported }

func uninstallAutostart() error { return errPlatformUnsupported }

func autostartEnabled() (bool, error) { return false, nil }
