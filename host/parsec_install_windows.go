//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	parsecInstallerURL    = "https://builds.parsec.app/vdd/parsec-vdd-0.41.0.0.exe"
	parsecInstallerSHA256 = "317977c5abd58f1c3f7b27ef7f96947efd07e6d35b27e415cea5daa6007629b1"
	parsecOwnedMarkerName = "parsec-vdd-installed-by-drawsplay"
)

type windowsDriverSetupResult struct {
	status      string
	parsecOwned bool
}

func setupWindowsDisplayDriver(installIDD func() string, iddReady func() bool,
	installParsec func() (bool, error)) windowsDriverSetupResult {
	status := installIDD()
	if iddReady() {
		return windowsDriverSetupResult{status: status}
	}
	log.Printf("DrawSplay virtual display setup: %s; trying signed Parsec VDD fallback", status)
	owned, err := installParsec()
	if err != nil {
		return windowsDriverSetupResult{status: fmt.Sprintf("%s; Parsec fallback failed: %v; mirror mode", status, err), parsecOwned: owned}
	}
	return windowsDriverSetupResult{status: status + "; Parsec fallback: installed", parsecOwned: owned}
}

func installWindowsDisplayDriver() windowsDriverSetupResult {
	return setupWindowsDisplayDriver(installIDDDriverPackage, func() bool {
		_, err := iddDevicePath()
		return err == nil
	}, installParsecFallback)
}

// Called only by elevated setup after the preferred driver failed. Runtime
// selection never downloads, installs or restarts a shared Parsec adapter.
func installParsecFallback() (bool, error) {
	if !isProcessElevated() {
		return false, fmt.Errorf("Parsec fallback installation requires administrator approval")
	}
	if _, err := parsecDevicePath(); err == nil {
		log.Println("Using existing Parsec VDD fallback; no download or install needed")
		return false, nil
	}
	_, _, haveUninstaller := parsecVDDUninstaller()
	if haveUninstaller || len(scanParsecVDDInfs()) > 0 {
		return false, fmt.Errorf("Parsec VDD is installed but unavailable; repair it or restart Windows (existing shared driver was not replaced)")
	}
	if !loadSettings().AllowToolAutoInstall {
		return false, fmt.Errorf("Parsec VDD is missing and automatic tool installation is disabled")
	}
	dir, err := driverStageDir()
	if err != nil {
		return false, fmt.Errorf("create protected Parsec staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("Remove temporary Parsec installer: %v", err)
		}
	}()
	installer := filepath.Join(dir, "parsec-vdd-0.41.0.0.exe")
	if err := downloadParsecInstaller(installer); err != nil {
		return false, err
	}
	if err := verifyParsecInstaller(installer); err != nil {
		return false, fmt.Errorf("refusing to execute Parsec fallback installer: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, installer, "/S")
	hideChildWindow(cmd)
	out, runErr := cmd.CombinedOutput()
	// Record ownership even if the installer exits abnormally after adding
	// its package, so a later uninstall can offer accurate cleanup.
	owned := len(scanParsecVDDInfs()) > 0
	if owned {
		stateDir, err := configDir()
		if err != nil {
			return true, fmt.Errorf("record Parsec fallback ownership: %w", err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, parsecOwnedMarkerName), []byte("installed by DrawSplay\n"), 0o600); err != nil {
			return true, fmt.Errorf("record Parsec fallback ownership: %w", err)
		}
	}
	if runErr != nil {
		return owned, fmt.Errorf("Parsec installer failed: %w (%s)", runErr, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := parsecDevicePath(); err == nil {
			return owned, nil
		}
		if time.Now().After(deadline) {
			return owned, fmt.Errorf("Parsec installed but its control interface is unavailable; restart Windows and retry")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func downloadParsecInstaller(path string) error {
	log.Printf("Downloading fallback driver only after DrawSplay driver failure: %s", parsecInstallerURL)
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(parsecInstallerURL)
	if err != nil {
		return fmt.Errorf("download Parsec fallback: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download Parsec fallback: HTTP %s", resp.Status)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	const limit = 64 << 20
	n, copyErr := io.Copy(file, io.LimitReader(resp.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download Parsec fallback: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if n > limit {
		return fmt.Errorf("Parsec installer exceeds 64 MiB limit")
	}
	return nil
}

func verifyParsecInstaller(path string) error {
	if err := verifyFileSHA256(path, parsecInstallerSHA256); err != nil {
		return err
	}
	script := "$ErrorActionPreference='Stop'; $s=Get-AuthenticodeSignature -LiteralPath $env:DRAWSPLAY_PARSEC_INSTALLER; " +
		"if($s.Status -ne 'Valid' -or -not $s.SignerCertificate -or " +
		"$s.SignerCertificate.GetNameInfo([System.Security.Cryptography.X509Certificates.X509NameType]::SimpleName,$false) -ne 'Parsec Cloud, Inc.') " +
		"{ throw ('Parsec installer publisher verification failed: '+$s.Status) }"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, windowsPowerShellPath(), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "DRAWSPLAY_PARSEC_INSTALLER="+path)
	hideChildWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("verify Parsec Authenticode signature: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
