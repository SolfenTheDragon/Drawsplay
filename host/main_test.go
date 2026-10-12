package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// The web UI re-runs the dependency checks (adb, ffmpeg, xrandr...) in
	// the background after a change; in tests that would outlive the test
	// and race the ones stubbing those tools.
	runBackgroundPreflight = func(settings) {}
	os.Exit(m.Run())
}
