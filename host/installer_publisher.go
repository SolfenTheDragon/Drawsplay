package main

import (
	"errors"
	"fmt"
	"strings"
)

// authenticodeInfo is a Windows executable's Authenticode verdict: Status
// as Get-AuthenticodeSignature names it ("Valid", "NotSigned",
// "HashMismatch", ...) and the signer certificate's thumbprint, if any.
type authenticodeInfo struct {
	Status     string
	Thumbprint string
}

// installerPublisherAllowed decides whether an installer may replace the
// installed host. It keeps the installed copy's publisher:
//
//   - installed host unsigned (every official release: their trust anchor is
//     the Ed25519 signature on SHA256SUMS.txt, checked by the updater):
//     any installer;
//   - installed host validly signed: only an installer validly signed with
//     the same certificate, so a self-signed build is never replaced by an
//     unsigned or differently signed one;
//   - installed host with a present but invalid signature (tampered, or an
//     untrusted signer): nothing.
func installerPublisherAllowed(current, installer authenticodeInfo) error {
	switch current.Status {
	case "NotSigned":
		return nil
	case "Valid":
		if current.Thumbprint == "" {
			return errors.New("the installed host's signature has no signer certificate")
		}
	default:
		return fmt.Errorf("the installed host has an invalid signature (%s); reinstall DrawSplay from the release page", current.Status)
	}
	if installer.Status != "Valid" || !strings.EqualFold(installer.Thumbprint, current.Thumbprint) {
		return fmt.Errorf("the installer is not signed by the publisher of this copy (%s)", installer.Status)
	}
	return nil
}
