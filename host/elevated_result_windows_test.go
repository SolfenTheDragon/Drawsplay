//go:build windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSplitElevatedResultPathRejectsNonLocalPaths(t *testing.T) {
	for _, bad := range []string{``, `result.json`, `\\server\share\result.json`, `\\?\C:\x\result.json`, `C:\`, `C:\dir\`, `C:relative.json`} {
		if _, _, err := splitElevatedResultPath(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	dir, name, err := splitElevatedResultPath(`C:\Users\x\AppData\Roaming\DrawSplay\winusb-bind.json`)
	if err != nil || dir != `C:\Users\x\AppData\Roaming\DrawSplay` || name != "winusb-bind.json" {
		t.Fatalf("got %q %q %v", dir, name, err)
	}
}

func TestWriteElevatedResultWritesIntoRealFolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "result.json")
	// A stale file under the final name (even a symlink is just replaced).
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := helperTaskResult{Error: "boom"}
	if err := writeElevatedResult(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got helperTaskResult
	if err := json.Unmarshal(data, &got); err != nil || got.Error != "boom" {
		t.Fatalf("read back %q: %v", data, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestWriteElevatedResultRefusesJunctionedFolder(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(base, "DrawSplay")
	// Junctions need no privilege, which is exactly why they matter here.
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v (%s)", err, out)
	}
	err := writeElevatedResult(filepath.Join(junction, "result.json"), helperTaskResult{})
	if err == nil {
		t.Fatal("write through a junction succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(target, "result.json")); statErr == nil {
		t.Fatal("result landed in the junction target")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("junction target not empty: %v", entries)
	}
}
