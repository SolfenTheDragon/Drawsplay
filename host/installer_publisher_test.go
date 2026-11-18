package main

import "testing"

func TestInstallerPublisherAllowed(t *testing.T) {
	signed := authenticodeInfo{Status: "Valid", Thumbprint: "AB12"}
	unsigned := authenticodeInfo{Status: "NotSigned"}
	for _, test := range []struct {
		name               string
		current, installer authenticodeInfo
		ok                 bool
	}{
		{"unsigned to unsigned (official releases)", unsigned, unsigned, true},
		{"unsigned to signed", unsigned, signed, true},
		{"signed to same publisher", signed, authenticodeInfo{Status: "Valid", Thumbprint: "ab12"}, true},
		{"signed to unsigned", signed, unsigned, false},
		{"signed to other publisher", signed, authenticodeInfo{Status: "Valid", Thumbprint: "CD34"}, false},
		{"signed to tampered installer", signed, authenticodeInfo{Status: "HashMismatch", Thumbprint: "AB12"}, false},
		{"signed without certificate", authenticodeInfo{Status: "Valid"}, signed, false},
		{"tampered installed host", authenticodeInfo{Status: "HashMismatch"}, unsigned, false},
		{"untrusted installed signer", authenticodeInfo{Status: "NotTrusted", Thumbprint: "AB12"}, signed, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := installerPublisherAllowed(test.current, test.installer); (err == nil) != test.ok {
				t.Fatalf("installerPublisherAllowed = %v, want allowed=%v", err, test.ok)
			}
		})
	}
}
