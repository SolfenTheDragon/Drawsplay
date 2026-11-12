package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayHelperMustBeRootOwnedAndNotWritable(t *testing.T) {
	if _, ok := displayHelperAt(filepath.Join(t.TempDir(), "missing")); ok {
		t.Fatal("a missing helper was accepted")
	}
	mine := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(mine, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := displayHelperAt(mine); ok && os.Geteuid() != 0 {
		t.Fatal("a helper owned by an ordinary user must not be run as root")
	}
	// A root-owned system executable passes the same check.
	sys, err := filepath.EvalSymlinks("/usr/bin/env")
	if err != nil {
		t.Skip(err)
	}
	if _, ok := displayHelperAt(sys); !ok {
		t.Fatal("a root-owned, read-only executable was refused")
	}
}

// The helper writes the connector udev rule itself after its first root run,
// so the copy it carries must be the packaged rule.
func TestDisplayHelperEmbedsThePackagedConnectorRule(t *testing.T) {
	rules, err := os.ReadFile(filepath.Join("..", "packaging", "linux", "70-drawsplay-drm-connector.rules"))
	if err != nil {
		t.Skip("packaging files not present")
	}
	var rule string
	for _, line := range strings.Split(string(rules), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			rule = line
		}
	}
	if rule == "" {
		t.Fatal("no rule line in 70-drawsplay-drm-connector.rules")
	}
	helper, err := os.ReadFile(filepath.Join("..", "packaging", "linux", "drawsplay-display-helper"))
	if err != nil {
		t.Fatal(err)
	}
	// In the helper the rule sits inside single quotes, so its own single
	// quotes are written as '"'"'.
	quoted := "DRM_RULE='" + strings.ReplaceAll(rule, "'", `'"'"'`) + "'"
	if !strings.Contains(string(helper), quoted) {
		t.Fatalf("drawsplay-display-helper does not carry the packaged rule; expected line:\n%s", quoted)
	}
}
