package main

import (
	"encoding/json"
	"slices"
	"testing"
)

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// The settings page (webui/app.js) reads these names.
func TestStatusAndDiagnosticsKeepTheirJSONNames(t *testing.T) {
	status := []string{"activeClient", "activeInputDevice", "activeTransport", "adb", "adbTunnelEnabled", "aoa", "aoaEnabled",
		"autostart", "autostartImplementation", "clientVideoStats", "connectionKind", "desktopHost", "featureFlags", "hostOS",
		"inputLocked", "maxFps", "metrics", "setup", "version", "video", "warnings"}
	if got := jsonKeys(t, hostStatus{}); !slices.Equal(got, status) {
		t.Errorf("status keys = %v\nwant %v", got, status)
	}
	diagnostics := []string{"activeClient", "activeInputDevice", "adb", "aoa", "clientVideoStats", "generatedAt", "inputLocked",
		"logCursor", "logLines", "metrics", "runtime", "settings", "setup", "video", "warnings"}
	if got := jsonKeys(t, hostDiagnostics{}); !slices.Equal(got, diagnostics) {
		t.Errorf("diagnostics keys = %v\nwant %v", got, diagnostics)
	}
}
