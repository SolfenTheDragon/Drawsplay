package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	hostIDOnce  sync.Once
	hostIDValue string
)

// hostID returns a random identifier, generated once and persisted in the
// config directory, that tablets use to keep per-host display preferences.
func hostID() string {
	hostIDOnce.Do(func() {
		hostIDValue = loadOrCreateHostID()
	})
	return hostIDValue
}

func loadOrCreateHostID() string {
	dir, err := configDir()
	if err == nil {
		path := filepath.Join(dir, "host_id")
		if data, readErr := os.ReadFile(path); readErr == nil {
			if id := strings.TrimSpace(string(data)); validHostID(id) {
				return id
			}
		}
		id := newHostID()
		if writeErr := os.WriteFile(path, []byte(id+"\n"), 0o600); writeErr != nil {
			fmt.Fprintf(os.Stderr, "host id not persisted (%v); tablets will not remember per-host settings\n", writeErr)
		}
		return id
	}
	return newHostID()
}

func validHostID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func newHostID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("host id: %v", err))
	}
	return hex.EncodeToString(b[:])
}

func hostDisplayName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}
