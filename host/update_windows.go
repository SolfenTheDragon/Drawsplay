//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// updateWaitPIDEnv tells a release installer started by the updater which
// host process to wait for before it stops the installed copy, so that copy
// can shut down cleanly (see runInstall).
const updateWaitPIDEnv = "DRAWSPLAY_UPDATE_WAIT_PID"

func updateBundleName(tag string) string { return "DrawSplay-windows-" + tag + ".zip" }

func updateInstallerName(tag string) string { return "drawsplay_installer_" + tag + ".exe" }

func updateInstallSupport() (bool, string) {
	m, installed := loadInstallManifest()
	exe, err := os.Executable()
	if !installed || err != nil || m.InstallDir == "" || !pathWithin(exe, m.InstallDir) {
		return false, "This copy was not installed with the DrawSplay installer. Download the update from the release page instead."
	}
	return true, "Windows asks for administrator approval once, then DrawSplay restarts. A connected tablet reconnects after a few seconds."
}

// applyUpdate extracts the release installer and starts it as a quiet
// reinstall. The installer waits for this process to exit, replaces the
// installed copy and starts the new one. sums is the verified
// SHA256SUMS.txt; releases that list the installer itself get it checked
// after extraction (older ones are covered by the verified bundle hash).
//
// The installer runs from the user's temp folder, so another program of the
// same user could still swap it before the administrator prompt, as it
// could a manual download; without an Authenticode signature Windows names
// no publisher on that prompt either way.
func applyUpdate(bundlePath, tag string, sums map[string]string) (applyResult, error) {
	dir := filepath.Dir(bundlePath)
	name := updateInstallerName(tag)
	err := extractZipSubset(bundlePath, dir, func(entry string) (string, bool) {
		return name, strings.EqualFold(filepath.Base(filepath.FromSlash(entry)), name)
	})
	if err != nil {
		return applyResult{}, fmt.Errorf("extract %s: %w", name, err)
	}
	installer := filepath.Join(dir, name)
	if want, ok := sums[name]; ok {
		if err := verifyFileSHA256(installer, want); err != nil {
			return applyResult{}, fmt.Errorf("verify %s: %w", name, err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return applyResult{}, err
	}
	if err := verifyUpdateInstallerSigner(exe, installer); err != nil {
		return applyResult{}, err
	}

	cmd := exec.Command(installer, "-install", "-quiet")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), updateWaitPIDEnv+"="+strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return applyResult{}, fmt.Errorf("start the installer: %w", err)
	}
	_ = cmd.Process.Release()
	return applyResult{keepDir: true, restart: func(stop func()) { stop() }}, nil
}

// verifyUpdateInstallerSigner applies installerPublisherAllowed to the
// installed host and the new installer.
func verifyUpdateInstallerSigner(current, installer string) error {
	cur, err := authenticodeSignature(current)
	if err != nil {
		return fmt.Errorf("read the installed host's signature: %w", err)
	}
	var next authenticodeInfo
	if cur.Status != "NotSigned" {
		if next, err = authenticodeSignature(installer); err != nil {
			return fmt.Errorf("read the installer's signature: %w", err)
		}
	}
	if err := installerPublisherAllowed(cur, next); err != nil {
		return err
	}
	if cur.Status == "NotSigned" {
		log.Printf("Installed host is not Authenticode-signed; the installer is trusted through the release checksum signature")
	}
	return nil
}

// authenticodeSignature reads a file's Authenticode status and signer
// thumbprint (replaced in tests).
var authenticodeSignature = func(path string) (authenticodeInfo, error) {
	script := "$ErrorActionPreference='Stop'; " +
		"$s=Get-AuthenticodeSignature -LiteralPath $env:DRAWSPLAY_SIGNED_FILE; " +
		"'STATUS='+$s.Status; if($s.SignerCertificate){ 'THUMBPRINT='+$s.SignerCertificate.Thumbprint }"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := powerShellCommand(ctx, script, "DRAWSPLAY_SIGNED_FILE="+path).CombinedOutput()
	if err != nil {
		return authenticodeInfo{}, fmt.Errorf("%w (%s)", err, strings.TrimSpace(string(out)))
	}
	var info authenticodeInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "STATUS="); ok {
			info.Status = v
		} else if v, ok := strings.CutPrefix(line, "THUMBPRINT="); ok {
			info.Thumbprint = v
		}
	}
	if info.Status == "" {
		return authenticodeInfo{}, fmt.Errorf("no signature status in output (%s)", strings.TrimSpace(string(out)))
	}
	return info, nil
}

// The Windows installer restarts the host itself.
func execPendingRestart() {}
