//go:build linux

package main

import (
	"os"
	"testing"
)

func TestConnectorWriteSkipsPrivilegeWhenDirectWriteWorks(t *testing.T) {
	old := connectorDirectWrite
	t.Cleanup(func() { connectorDirectWrite = old })
	var gotPath, gotVal string
	connectorDirectWrite = func(path, value string) error { gotPath, gotVal = path, value; return nil }
	const path = "/sys/class/drm/card0-HDMI-A-1/status"
	if os.Geteuid() == 0 {
		t.Skip("root writes directly anyway")
	}
	if err := writeConnectorStatusPrivileged(path, "on", false); err != nil {
		t.Fatalf("direct write should satisfy the request without sudo/pkexec: %v", err)
	}
	if gotPath != path || gotVal != "on" {
		t.Fatalf("direct write got %q %q", gotPath, gotVal)
	}
}
