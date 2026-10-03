//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

const vddCustomResolutionSetupTimeout = 90 * time.Second

func runVDDAdminMode(setup bool, width, height int, restart bool) bool {
	if setup {
		if width <= 0 || height <= 0 {
			log.Fatalf("invalid elevated VDD custom-resolution args: width=%d height=%d", width, height)
		}
		if err := performVDDCustomResolutionAdminSetup(width, height); err != nil {
			log.Fatalf("elevated VDD custom-resolution setup failed: %v", err)
		}
		log.Printf("Elevated VDD custom-resolution setup completed for %dx%d", width, height)
		return true
	}
	if restart {
		if err := restartVDDAdapter(); err != nil {
			log.Fatalf("elevated VDD adapter restart failed: %v", err)
		}
		log.Printf("Elevated VDD adapter restart completed")
		return true
	}
	return false
}

func ensureVDDCustomResolutionWritable(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid resolution %dx%d", width, height)
	}
	if err := writeVDDCustomResolution(width, height); err == nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve host executable for elevation: %w", err)
	}
	args := fmt.Sprintf(
		"-vdd-custom-resolution-admin-setup -vdd-custom-resolution-width %d -vdd-custom-resolution-height %d",
		width, height,
	)
	if err := runElevatedAndWait(exe, args, vddCustomResolutionSetupTimeout); err != nil {
		return fmt.Errorf("elevated setup did not complete: %w", err)
	}
	if err := writeVDDCustomResolution(width, height); err != nil {
		return fmt.Errorf("VDD key still not writable after elevated setup: %w", err)
	}
	return nil
}

func restartVDDAdapterAuto() error {
	if err := restartVDDAdapter(); err != nil {
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return fmt.Errorf("resolve host executable for VDD restart elevation: %w", exeErr)
		}
		if err := runElevatedAndWait(exe, "-vdd-restart-adapter", vddCustomResolutionSetupTimeout); err != nil {
			return fmt.Errorf("elevated VDD adapter restart did not complete: %w", err)
		}
	}
	// Whichever path restarted the adapter, wait for the control device
	// interface to come back before callers try to reopen it.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := vddDevicePath(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("VDD device interface did not return after adapter restart")
}

func performVDDCustomResolutionAdminSetup(width, height int) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, vddCustomResolutionRegPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create/open HKLM\\%s: %w", vddCustomResolutionRegPath, err)
	}
	_ = key.Close()

	if err := writeVDDCustomResolution(width, height); err != nil {
		return err
	}
	if err := grantUsersVDDCustomResolutionSetValue(); err != nil {
		return err
	}
	return nil
}

func grantUsersVDDCustomResolutionSetValue() error {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		return fmt.Errorf("SystemRoot is unavailable")
	}
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if !fileExists(powershell) {
		return fmt.Errorf("trusted PowerShell executable is unavailable")
	}

	script := strings.Join([]string{
		"$path='HKLM:\\SOFTWARE\\Parsec\\vdd\\0'",
		"$acl=Get-Acl -Path $path",
		"$rule=New-Object System.Security.AccessControl.RegistryAccessRule('Users','SetValue','Allow')",
		"$acl.SetAccessRule($rule)",
		"Set-Acl -Path $path -AclObject $acl",
	}, "; ")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("grant Users SetValue on HKLM\\%s: %w (%s)", vddCustomResolutionRegPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func restartVDDAdapter() error {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		return fmt.Errorf("SystemRoot is unavailable")
	}
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if !fileExists(powershell) {
		return fmt.Errorf("trusted PowerShell executable is unavailable")
	}

	script := strings.Join([]string{
		"$dev=Get-PnpDevice -PresentOnly | Where-Object { $_.FriendlyName -like 'Parsec Virtual Display Adapter*' } | Select-Object -First 1",
		"if($null -eq $dev){Write-Error 'Parsec Virtual Display Adapter not found'; exit 2}",
		"Disable-PnpDevice -InstanceId $dev.InstanceId -Confirm:$false -ErrorAction Stop",
		"Start-Sleep -Milliseconds 700",
		"Enable-PnpDevice -InstanceId $dev.InstanceId -Confirm:$false -ErrorAction Stop",
	}, "; ")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart Parsec VDD adapter: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
