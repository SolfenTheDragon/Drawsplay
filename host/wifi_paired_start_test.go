package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPairedWiFiStartIsInertWithoutFlagInterfaceOrTrust(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("APPDATA", cfg)
	t.Setenv(wifiFeatureFlagEnv, "")
	t.Setenv(wifiPairedInterfaceEnv, "lo")
	if started := startPairedWiFiRuntime(context.Background(), nil); started != nil {
		t.Fatal("started with the feature flag off")
	}
	t.Setenv(wifiFeatureFlagEnv, "1")
	t.Setenv(wifiPairedInterfaceEnv, "")
	if startPairedWiFiRuntime(context.Background(), nil) != nil {
		t.Fatal("started without an explicit interface")
	}
	t.Setenv(wifiPairedInterfaceEnv, "lo")
	if startPairedWiFiRuntime(context.Background(), nil) != nil {
		t.Fatal("started without a pairing")
	}
	entries, _ := os.ReadDir(filepath.Join(cfg, "DrawSplay", wifiTrustDirName))
	if len(entries) != 0 {
		t.Fatal("inert start created trust material")
	}
}
