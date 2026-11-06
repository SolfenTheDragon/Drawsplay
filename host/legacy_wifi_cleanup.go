package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
)

// The token-based Wi-Fi transport (wifiEnabled, wifiAuthToken, a TOFU TLS
// certificate) was removed in favour of the paired mutual-TLS transport.
// removeLegacyWiFiFiles deletes what it left in the config folder: its TLS
// key pair, and the saved bearer token inside config.json (rewritten without
// the old keys, which the settings struct no longer has).
func removeLegacyWiFiFiles() {
	path, err := configPath()
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	for _, name := range []string{"wifi_tls_cert.pem", "wifi_tls_key.pem"} {
		p := filepath.Join(dir, name)
		if err := os.Remove(p); err == nil {
			log.Printf("Removed %s (left by the retired token-based Wi-Fi transport)", p)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte(`"wifiAuthToken"`)) && !bytes.Contains(data, []byte(`"wifiEnabled"`)) {
		return
	}
	if err := saveSettings(loadSettings()); err != nil {
		log.Printf("Could not remove retired Wi-Fi settings from %s: %v", path, err)
		return
	}
	log.Printf("Removed the retired token-based Wi-Fi settings from %s", path)
}
