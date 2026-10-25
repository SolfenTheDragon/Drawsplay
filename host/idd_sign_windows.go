//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const iddSignerSubject = "CN=DrawSplay Virtual Display Driver Signing"

func verifyEmbeddedIDDPackage(dir string) error {
	for _, name := range []string{iddInfName, "DrawSplayIdd.dll", "DrawSplayIdd.cat"} {
		want, err := embeddedDriver.ReadFile("driver/idd/" + name)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", name, err)
		}
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read staged %s: %w", name, err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("staged %s differs from the embedded driver; refusing to sign", name)
		}
	}
	return nil
}

func signIDDDriverPackage(dir string) (string, error) {
	if !isProcessElevated() {
		return "", errors.New("virtual display driver signing requires administrator approval")
	}
	if err := verifyEmbeddedIDDPackage(dir); err != nil {
		return "", err
	}
	return signLocalDriverCatalog(dir, "", "DrawSplayIdd.cat", iddSignerSubject,
		"DrawSplay virtual display driver (this PC only)")
}

func removeIDDSigningCerts(thumbprint string) error {
	return removeDriverSigningCertsMatching("$_.Subject -like '" + iddSignerSubject +
		"*' -and ('" + thumbprint + "' -eq '' -or $_.Thumbprint -eq '" + thumbprint + "')")
}

type iddPackageInstallOps struct {
	sign       func() (string, error)
	stage      func() error
	device     func() (bool, error)
	removeCert func(string) error
}

func installLocallySignedIDD(ops iddPackageInstallOps) (bool, error) {
	thumbprint, err := ops.sign()
	if err != nil {
		return false, fmt.Errorf("sign virtual display catalog: %w", err)
	}
	if err := ops.stage(); err != nil {
		if thumbprint != "" {
			err = errors.Join(err, ops.removeCert(thumbprint))
		}
		return false, fmt.Errorf("stage virtual display driver: %w", err)
	}
	// Once staged, retain its certificate even if device creation fails:
	// Windows may need it on retry/reboot. Uninstall removes package and trust.
	return ops.device()
}
