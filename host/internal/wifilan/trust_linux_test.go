package wifilan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustStoreRejectsPublicPermissionsAndSymlinkedPaths(t *testing.T) {
	for _, kind := range []string{"public-directory", "public-file", "directory-link", "record-link"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			directory := filepath.Join(parent, "wifi")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "public-directory":
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.WriteFile(filepath.Join(directory, "trust.json"), []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			case "directory-link":
				link := filepath.Join(parent, "linked-wifi")
				if err := os.Symlink(directory, link); err != nil {
					t.Fatal(err)
				}
				directory = link
			case "record-link":
				target := filepath.Join(parent, "outside")
				if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(directory, "trust.json")); err != nil {
					t.Fatal(err)
				}
			}
			if store, err := OpenTrustStore(directory); err == nil {
				store.Close()
				t.Fatal("unsafe identity storage accepted")
			}
		})
	}
}
