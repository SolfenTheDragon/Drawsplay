package main

// Small file-system helpers shared across the host.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// writeFileAtomic replaces path with data so that readers see either the old
// or the new content, never a partial file, even after a crash: data goes to
// a uniquely named temp file in the same folder, is synced, then renamed over
// path. The folder is created (0700) when missing. The file mode is perm.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// writeJSONAtomic writes v as indented JSON with a final newline (mode 0600)
// through writeFileAtomic.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

// pathWithin reports whether path is root or lies beneath it. It compares
// case-insensitively, as Windows does; every caller handles Windows paths
// (install folders, archive extraction).
func pathWithin(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	r, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(strings.ToLower(r), strings.ToLower(p))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// fileExists reports whether path is an existing regular file (not a folder).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// fileSHA256 returns the lowercase hex SHA-256 of a file's content.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
