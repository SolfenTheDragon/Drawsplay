//go:build windows

package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestBundle(t *testing.T, tag string, installer []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), updateBundleName(tag))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("DrawSplay-windows-" + tag + "/" + updateInstallerName(tag))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(installer); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// An installer that does not match its line in the verified SHA256SUMS.txt
// is refused before anything inspects or starts it.
func TestApplyUpdateRefusesInstallerWithWrongChecksum(t *testing.T) {
	defer func(old func(string) (authenticodeInfo, error)) { authenticodeSignature = old }(authenticodeSignature)
	authenticodeSignature = func(string) (authenticodeInfo, error) {
		t.Fatal("signature checked before the installer checksum")
		return authenticodeInfo{}, nil
	}
	const tag = "v9.9.9"
	bundle := writeTestBundle(t, tag, []byte("not the released installer"))
	sums := map[string]string{updateInstallerName(tag): strings.Repeat("0", 64)}
	_, err := applyUpdate(bundle, tag, sums)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("applyUpdate = %v, want a checksum mismatch", err)
	}
}
