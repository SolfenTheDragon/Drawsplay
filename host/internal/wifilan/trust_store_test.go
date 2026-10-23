package wifilan

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustStorePersistsIdentityAndRevocationWithoutRotatingKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wifi")
	store, err := OpenTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	identity := store.Identity()
	first := sha256.Sum256(identity.Certificate[0])
	if _, err := OpenTrustStore(dir); err == nil {
		t.Fatal("concurrent store writer accepted")
	}
	pin := sha256.Sum256([]byte("tablet public key"))
	if err := store.Pair(pin, "Tablet"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	identity = store.Identity()
	if sha256.Sum256(identity.Certificate[0]) != first {
		t.Fatal("host identity rotated across restart")
	}
	if got, ok := store.Peer(); !ok || got != pin {
		t.Fatal("paired tablet not restored")
	}
	if err := store.Revoke(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenTrustStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, paired := store.Peer(); paired {
		t.Fatal("revoked peer restored")
	}
}

func TestTrustStoreDoesNotResetCorruptOrOversizedIdentity(t *testing.T) {
	for _, content := range [][]byte{[]byte("{"), []byte(`{"version":99}`), bytes.Repeat([]byte("x"), 16385)} {
		dir := t.TempDir()
		path := filepath.Join(dir, "trust.json")
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		if store, err := OpenTrustStore(dir); err == nil {
			store.Close()
			t.Fatal("invalid trust silently repaired")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, content) {
			t.Fatal("invalid identity overwritten instead of reporting repair needed")
		}
	}
}

func TestTrustStoreMissingRecordDoesNotRotateExistingIdentity(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wifi")
	store, err := OpenTrustStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "trust.json")); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenTrustStore(directory)
	if err == nil {
		reopened.Close()
		t.Fatal("missing record in an existing store silently rotated host identity")
	}
	if _, err := os.Stat(filepath.Join(directory, "trust.json")); !os.IsNotExist(err) {
		t.Fatal("missing identity was replaced without explicit local repair")
	}
}

func TestTrustStoreRejectsInvalidPairingWithoutLosingExistingPeer(t *testing.T) {
	store, err := OpenTrustStore(filepath.Join(t.TempDir(), "wifi"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pin := sha256.Sum256([]byte("tablet"))
	if err := store.Pair(pin, "Tablet"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct {
		pin   [32]byte
		label string
	}{
		{[32]byte{}, "Tablet"},
		{pin, string(bytes.Repeat([]byte("x"), 64))},
		{pin, "Tablet\nspoofed log"},
		{pin, ""},
	} {
		if err := store.Pair(invalid.pin, invalid.label); err == nil {
			t.Fatal("invalid trust record persisted")
		}
		if got, ok := store.Peer(); !ok || got != pin {
			t.Fatal("failed trust update lost existing peer")
		}
	}
}
