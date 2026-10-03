//go:build windows

package main

// Automatic Parsec VDD driver installation.
//
// Extend mode needs the Parsec Virtual Display Adapter driver (see
// vdd_windows.go). If it is missing at startup the host downloads the
// pinned installer and runs it elevated and silent (NSIS /S), so first-run
// users get extend mode without a manual step. Version 0.41 is pinned:
// 0.45 has a known incompatibility with recent Windows 11 builds where the
// added monitor never activates.
//
// Disable with -no-driver-install (the host then falls back to mirror mode
// as before).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	vddInstallerURL    = "https://builds.parsec.app/vdd/parsec-vdd-0.41.0.0.exe"
	vddInstallerName   = "parsec-vdd-0.41.0.0.exe"
	vddInstallerSHA256 = "317977c5abd58f1c3f7b27ef7f96947efd07e6d35b27e415cea5daa6007629b1"
	vddSignerSubject   = "Parsec Cloud, Inc."

	// Generous cap on the elevated installer run; NSIS /S normally
	// finishes in a few seconds.
	vddInstallTimeout = 3 * time.Minute
)

// ensureVDDDriver installs the Parsec VDD driver if it is not present.
// Best-effort: on any failure it logs and returns, leaving the host to run
// in mirror mode. Called once at startup before the video listener starts.
func ensureVDDDriver(disabled bool) {
	if disabled {
		log.Println("VDD driver auto-install disabled (-no-driver-install)")
		return
	}
	if _, err := vddDevicePath(); err == nil {
		return // driver present
	}
	if !shouldOfferFirstRunVDDInstall() {
		log.Println("Parsec VDD driver not found; installing (needed for extend mode)")
	} else if !promptInstallVDDOnFirstRun() {
		log.Println("Parsec VDD driver install declined; continuing in mirror mode")
		return
	}

	installer, err := downloadVDDInstaller()
	if err != nil {
		log.Printf("VDD installer download failed (%v); continuing in mirror mode", err)
		return
	}
	if err := verifyVDDInstaller(installer); err != nil {
		log.Printf("VDD installer verification failed (%v); refusing elevated execution", err)
		return
	}

	if err := runElevatedAndWait(installer, "/S", vddInstallTimeout); err != nil {
		log.Printf("VDD driver install failed (%v); continuing in mirror mode", err)
		return
	}

	// The device interface can take a moment to appear after the installer
	// exits; retry briefly.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := vddDevicePath(); err == nil {
			log.Println("Parsec VDD driver installed successfully")
			return
		}
		if time.Now().After(deadline) {
			log.Println("VDD driver installed but device not yet available; a Windows reboot may be required for extend mode")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func shouldOfferFirstRunVDDInstall() bool {
	return os.Getenv(bgChildEnv) == "" && !isServiceManagedRun() && isFirstHostBoot()
}

func isServiceManagedRun() bool {
	for _, arg := range os.Args[1:] {
		if arg == "-background" || arg == "--background" {
			return true
		}
		if strings.HasPrefix(arg, "-service=") || strings.HasPrefix(arg, "--service=") {
			return true
		}
	}
	return false
}

func promptInstallVDDOnFirstRun() bool {
	fmt.Println("Parsec virtual display driver is not installed.")
	fmt.Print("Install it now for extend-mode support? [y/N]: ")
	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		answer = ""
	}

	decision := strings.EqualFold(strings.TrimSpace(answer), "y") ||
		strings.EqualFold(strings.TrimSpace(answer), "yes")
	if err := persistNoDriverInstallPreference(!decision); err != nil {
		log.Printf("Saving VDD install preference failed (%v)", err)
	}
	return decision
}

func isFirstHostBoot() bool {
	path, err := configPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func persistNoDriverInstallPreference(skipInstall bool) error {
	cfg := loadSettings()
	cfg.NoDriverInstall = skipInstall
	return saveSettings(cfg)
}

// downloadVDDInstaller fetches the pinned installer into the user temp dir,
// reusing a previous download if one exists.
func downloadVDDInstaller() (string, error) {
	dest := filepath.Join(os.TempDir(), vddInstallerName)
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 0 {
		if err := verifyVDDInstaller(dest); err == nil {
			log.Printf("Using verified cached VDD installer at %s", dest)
			return dest, nil
		}
		if err := os.Remove(dest); err != nil {
			return "", fmt.Errorf("remove untrusted cached installer: %w", err)
		}
	}

	log.Printf("Downloading %s", vddInstallerURL)
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(vddInstallerURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	tmp, err := os.CreateTemp(os.TempDir(), vddInstallerName+".part-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, resp.Body)
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := verifyVDDInstaller(tmp.Name()); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("download integrity/trust verification failed: %w", err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return dest, nil
}

// verifyVDDInstaller requires both the pinned artifact digest and a valid
// Windows Authenticode chain from the expected publisher before elevation.
func verifyVDDInstaller(path string) error {
	if err := verifyFileSHA256(path, vddInstallerSHA256); err != nil {
		return err
	}
	return verifyAuthenticodePublisher(path, vddSignerSubject)
}

func verifyAuthenticodePublisher(path, expectedSubject string) error {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		return fmt.Errorf("SystemRoot is unavailable")
	}
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if !fileExists(powershell) {
		return fmt.Errorf("trusted PowerShell executable is unavailable")
	}
	quotedPath := strings.ReplaceAll(path, "'", "''")
	quotedSubject := strings.ReplaceAll(expectedSubject, "'", "''")
	script := fmt.Sprintf(
		"$s=Get-AuthenticodeSignature -LiteralPath '%s'; "+
			"if($s.Status -ne 'Valid'){Write-Error ('Authenticode status: '+$s.Status); exit 1}; "+
			"if($null -eq $s.SignerCertificate -or $s.SignerCertificate.Subject -notlike '*%s*'){Write-Error 'Unexpected Authenticode publisher'; exit 2}",
		quotedPath, quotedSubject,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Authenticode verification: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// shellExecuteInfo mirrors SHELLEXECUTEINFOW for ShellExecuteExW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIconOrMon   windows.Handle
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040 // SEE_MASK_NOCLOSEPROCESS
	swHide                = 0
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

// runElevatedAndWait launches file with args via the "runas" verb (UAC
// elevation prompt) and waits for the process to exit or the timeout.
func runElevatedAndWait(file, args string, timeout time.Duration) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	filep, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	argsp, _ := windows.UTF16PtrFromString(args)

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       filep,
		lpParameters: argsp,
		nShow:        swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	ret, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return fmt.Errorf("elevation declined by user")
		}
		return fmt.Errorf("ShellExecuteEx: %v", callErr)
	}
	if info.hProcess == 0 {
		return fmt.Errorf("installer started but no process handle returned")
	}
	defer windows.CloseHandle(info.hProcess)

	ev, err := windows.WaitForSingleObject(info.hProcess, uint32(timeout.Milliseconds()))
	if err != nil {
		return fmt.Errorf("waiting for installer: %w", err)
	}
	if ev == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("installer did not finish within %s", timeout)
	}

	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err == nil && code != 0 {
		return fmt.Errorf("installer exited with code %d", code)
	}
	return nil
}
