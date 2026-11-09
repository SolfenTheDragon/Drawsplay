//go:build windows

package main

import (
	"context"
	"fmt"
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
// installed copy and starts the new one.
func applyUpdate(bundlePath, tag string) (applyResult, error) {
	dir := filepath.Dir(bundlePath)
	name := updateInstallerName(tag)
	err := extractZipSubset(bundlePath, dir, func(entry string) (string, bool) {
		return name, strings.EqualFold(filepath.Base(filepath.FromSlash(entry)), name)
	})
	if err != nil {
		return applyResult{}, fmt.Errorf("extract %s: %w", name, err)
	}
	installer := filepath.Join(dir, name)
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

// verifyUpdateInstallerSigner requires a code-signed host to update only to
// an installer signed by the same publisher. Unsigned copies (local and
// unsigned release builds) rely on the SHA-256 check alone.
func verifyUpdateInstallerSigner(current, installer string) error {
	script := "$ErrorActionPreference='Stop'; " +
		"$cur=Get-AuthenticodeSignature -LiteralPath $env:DRAWSPLAY_CURRENT_EXE; " +
		"if($cur.Status -ne 'Valid' -or -not $cur.SignerCertificate){ exit 0 }; " +
		"$new=Get-AuthenticodeSignature -LiteralPath $env:DRAWSPLAY_UPDATE_INSTALLER; " +
		"if($new.Status -ne 'Valid' -or -not $new.SignerCertificate -or " +
		"$new.SignerCertificate.Subject -ne $cur.SignerCertificate.Subject) " +
		"{ throw ('the installer is not signed by the publisher of this copy ('+$new.Status+')') }"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := powerShellCommand(ctx, script, "DRAWSPLAY_CURRENT_EXE="+current, "DRAWSPLAY_UPDATE_INSTALLER="+installer)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("verify the installer's signature: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// The Windows installer restarts the host itself.
func execPendingRestart() {}
