package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.bin")
	if err := os.WriteFile(path, []byte("trusted artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	const digest = "29dcd450ef0324ad1a5a65092c7c36a05d064b549c08d32b5583675e78cea936"
	if err := verifyFileSHA256(path, digest); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
}

func TestVerifyFileSHA256FailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.bin")
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{
		strings.Repeat("0", 64),
		"not-a-digest",
	} {
		if err := verifyFileSHA256(path, digest); err == nil {
			t.Errorf("digest %q unexpectedly accepted", digest)
		}
	}
	if err := verifyFileSHA256(filepath.Join(t.TempDir(), "missing"), strings.Repeat("0", 64)); err == nil {
		t.Error("missing artifact unexpectedly accepted")
	}
}
