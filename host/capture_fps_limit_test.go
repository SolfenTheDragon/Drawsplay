package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettingsClampsFPSToMaxCaptureFPS(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.FPS = 120
	data, _ := json.Marshal(s)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadSettings().FPS; got != maxCaptureFPS {
		t.Fatalf("loaded fps = %d, want clamped %d", got, maxCaptureFPS)
	}
	if err := defaultSettings().validate(); err != nil {
		t.Fatalf("default settings must validate: %v", err)
	}
	s.FPS = maxCaptureFPS + 1
	if err := s.validate(); err == nil {
		t.Fatalf("fps %d must be rejected", s.FPS)
	}
}
