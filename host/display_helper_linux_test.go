package main

import (
	"os"
	"path/filepath"
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
