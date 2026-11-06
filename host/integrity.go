package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// verifyFileSHA256 verifies a downloaded artifact against a pinned digest.
// Callers must fail closed and delete the artifact on any error.
func verifyFileSHA256(path, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("invalid pinned SHA-256 digest")
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("invalid pinned SHA-256 digest: %w", err)
	}

	actual, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("hash artifact for integrity verification: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("SHA-256 mismatch: got %s, want %s", actual, expected)
	}
	return nil
}
