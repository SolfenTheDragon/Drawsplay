package main

// Persisted Windows Display Settings choice for the DrawSplay virtual monitor.
//
// The driver offers the tablet's native size plus standard 720p-4K modes.
// When the user picks one in Windows Display Settings, the host records it
// here, keyed by the tablet's native size, and plugs the next session's
// monitor at that mode. Without a record the monitor starts at the native
// size. This deliberately does not trust the tablet's remembered mode
// (modePrefs), which can be stale from older host versions.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
)

type windowsDisplayMode struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

var windowsDisplayModeStore = struct {
	mu sync.Mutex
	// path returns the JSON file location; replaced in tests.
	path func() (string, error)
}{
	path: func() (string, error) {
		dir, err := configDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "display_modes.json"), nil
	},
}

func windowsDisplayModeKey(nativeW, nativeH int) string {
	return fmt.Sprintf("%dx%d", nativeW, nativeH)
}

func loadWindowsDisplayModes() (map[string]windowsDisplayMode, error) {
	path, err := windowsDisplayModeStore.path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]windowsDisplayMode{}, nil
	}
	if err != nil {
		return nil, err
	}
	modes := map[string]windowsDisplayMode{}
	if err := json.Unmarshal(data, &modes); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return modes, nil
}

// rememberedWindowsDisplayMode returns the mode last picked in Windows Display
// Settings for a tablet with the given native size.
func rememberedWindowsDisplayMode(nativeW, nativeH int) (w, h int, ok bool) {
	if nativeW <= 0 || nativeH <= 0 {
		return 0, 0, false
	}
	windowsDisplayModeStore.mu.Lock()
	defer windowsDisplayModeStore.mu.Unlock()
	modes, err := loadWindowsDisplayModes()
	if err != nil {
		log.Printf("Ignoring remembered Windows display modes: %v", err)
		return 0, 0, false
	}
	m, found := modes[windowsDisplayModeKey(nativeW, nativeH)]
	if !found || m.Width <= 0 || m.Height <= 0 {
		return 0, 0, false
	}
	return m.Width, m.Height, true
}

// rememberWindowsDisplayMode records the mode the user picked in Windows
// Display Settings. Picking the native size clears the record.
func rememberWindowsDisplayMode(nativeW, nativeH, w, h int) error {
	if nativeW <= 0 || nativeH <= 0 || w <= 0 || h <= 0 {
		return nil
	}
	windowsDisplayModeStore.mu.Lock()
	defer windowsDisplayModeStore.mu.Unlock()
	modes, err := loadWindowsDisplayModes()
	if err != nil {
		modes = map[string]windowsDisplayMode{}
	}
	key := windowsDisplayModeKey(nativeW, nativeH)
	if w == nativeW && h == nativeH {
		delete(modes, key)
	} else {
		modes[key] = windowsDisplayMode{Width: w, Height: h}
	}
	data, err := json.MarshalIndent(modes, "", "  ")
	if err != nil {
		return err
	}
	path, err := windowsDisplayModeStore.path()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
