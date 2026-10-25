//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDriverStageSecurityRejectsUnprivilegedAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		sddl string
		want bool
	}{
		{"restricted", "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", true},
		{"system owner", "O:SYG:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", true},
		{"users full control", "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;BU)", false},
		{"user owner can replace DACL", "O:S-1-5-21-1-2-3-1001G:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		{"inheritable unprotected DACL", "O:BAG:BAD:(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false},
		{"missing child protection", "O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)", false},
		{"null DACL", "O:BAG:BAD:NO_ACCESS_CONTROL", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateDriverStageSecurity(sd); (err == nil) != tc.want {
				t.Fatalf("security accepted=%v, want %v (%v)", err == nil, tc.want, err)
			}
		})
	}
}

func TestVerifyGeneratedWinUSBINF(t *testing.T) {
	dir := t.TempDir()
	want := []byte("generated in-box WinUSB binding")
	path := filepath.Join(dir, winusbPackageINFName)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyWinUSBPackageINF(dir, want); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("substituted package"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyWinUSBPackageINF(dir, want); err == nil {
		t.Fatal("accepted substituted WinUSB INF")
	}
}

func TestSigningRejectsUserWritableStage(t *testing.T) {
	// Rejection must happen before PowerShell can create/trust any certificate.
	if _, err := signWinUSBPackage(t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "protected staging") {
		t.Fatalf("expected protected staging rejection, got %v", err)
	}
}

func TestDriverStagingRequiresElevation(t *testing.T) {
	if isProcessElevated() {
		t.Skip("non-elevated entry point")
	}
	if dir, err := driverStageDir(); err == nil {
		os.RemoveAll(dir)
		t.Fatal("created privileged driver staging without elevation")
	} else if !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("expected actionable administrator error, got %v", err)
	}
}

// Opt-in: creates a restricted workspace and temporary catalog trust, but
// never stages a package in the driver store or touches tablet drivers.
func TestDriverStagingIntegration(t *testing.T) {
	if os.Getenv("DRAWSPLAY_TEST_DRIVER_STAGING") != "1" {
		t.Skip("set DRAWSPLAY_TEST_DRIVER_STAGING=1 in an elevated test process")
	}
	if !isProcessElevated() {
		t.Fatal("driver staging integration requires administrator approval")
	}
	dir, err := driverStageDir()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	}()
	if err := verifyDriverStageDir(dir); err != nil {
		t.Fatal(err)
	}
	inf := filepath.Join(dir, winusbPackageINFName)
	expected := []byte(aoaWinUSBPackageInf(aoaWinUSBHardwareIDs(nil), time.Now()))
	if err := os.WriteFile(inf, expected, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restrictedTokenStageAccess(dir, inf); err != nil {
		t.Fatal(err)
	}
	thumbprint, err := signWinUSBPackage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if thumbprint == "" {
		t.Fatal("new WinUSB catalog did not produce a signing certificate")
	}
	defer func() {
		if err := removeWinUSBSigningCerts(thumbprint); err != nil {
			t.Error(err)
		}
	}()
	if err := verifyWinUSBPackageINF(dir, expected); err != nil {
		t.Fatal(err)
	}
	t.Log("WinUSB catalog signed in an admin-owned workspace; restricted token could not alter files or directory ACL")
}

func restrictedTokenStageAccess(dir, inf string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	disabled := windows.SIDAndAttributes{Sid: admin}
	var impersonation windows.Token
	advapi := windows.NewLazySystemDLL("advapi32.dll")
	// Disable privileges and make Administrators deny-only, reproducing the
	// DACL boundary of a non-elevated token without using its identification-only linked handle.
	if result, _, err := advapi.NewProc("CreateRestrictedToken").Call(uintptr(token), 1,
		1, uintptr(unsafe.Pointer(&disabled)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&impersonation))); result == 0 {
		return fmt.Errorf("create unprivileged test token: %w", err)
	}
	defer impersonation.Close()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	impersonate := advapi.NewProc("ImpersonateLoggedOnUser")
	if result, _, err := impersonate.Call(uintptr(impersonation)); result == 0 {
		return fmt.Errorf("impersonate unprivileged test token: %w", err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			// Do not release an impersonated thread back to the runtime.
			panic(err)
		}
	}()
	for _, probe := range []struct {
		path   string
		access uint32
		flags  uint32
	}{
		{inf, windows.GENERIC_WRITE, 0},
		{inf, windows.WRITE_DAC, 0},
		{dir, windows.WRITE_DAC, windows.FILE_FLAG_BACKUP_SEMANTICS},
		{dir, windows.DELETE, windows.FILE_FLAG_BACKUP_SEMANTICS},
	} {
		ptr, err := windows.UTF16PtrFromString(probe.path)
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(ptr, probe.access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, probe.flags, 0)
		if err == nil {
			windows.CloseHandle(handle)
			return fmt.Errorf("restricted token could access %s with mask 0x%x", probe.path, probe.access)
		}
		if err != windows.ERROR_ACCESS_DENIED {
			return fmt.Errorf("expected access denied for %s, got %v", probe.path, err)
		}
	}
	return nil
}
