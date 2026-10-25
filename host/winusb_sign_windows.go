//go:build windows

package main

// Signed WinUSB driver package for the tablet's USB identities.
//
// Windows only installs a driver package whose catalog is signed by a
// trusted publisher; an unsigned INF fails with 0xE000022F. Instead of
// asking users to run Zadig, the elevated installer does what Zadig does:
//
//  1. generate the WinUSB INF (aoaWinUSBPackageInf) and its catalog,
//  2. create a self-signed code-signing certificate unique to this PC,
//  3. sign the catalog, trust the certificate (LocalMachine Root +
//     TrustedPublisher) and immediately delete its private key, so nothing
//     else can ever be signed with it,
//  4. add the package with pnputil.
//
// The package binds only in-box WinUSB (no DrawSplay kernel code). The
// uninstaller deletes the package and the certificate.

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	winusbSignerSubject  = "CN=DrawSplay USB Driver Signing"
	winusbBindTaskName   = `\DrawSplay\Bind Tablet USB Driver`
	winusbBindResultName = "winusb-bind-result.json"
	winusbBindTimeout    = 3 * time.Minute
	winusbPackageINFName = "drawsplay_winusb.inf"
)

// installSignedWinUSBPackage (elevated) replaces DrawSplay's WinUSB driver
// package with a freshly signed one covering the AOA identities plus extra
// (attached tablets' normal-mode identities).
func installSignedWinUSBPackage(extra [][2]uint16) error {
	if !isProcessElevated() {
		return errors.New("WinUSB driver setup must run as administrator")
	}
	previous, _ := scanAOAWinUSBInfs()

	dir, err := driverStageDir()
	if err != nil {
		return fmt.Errorf("create package dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("WinUSB: remove protected driver staging: %v", err)
		}
	}()
	inf := filepath.Join(dir, winusbPackageINFName)
	expectedINF := []byte(aoaWinUSBPackageInf(aoaWinUSBHardwareIDs(extra), time.Now()))
	if err := os.WriteFile(inf, expectedINF, 0o600); err != nil {
		return fmt.Errorf("write INF: %w", err)
	}
	if err := verifyWinUSBPackageINF(dir, expectedINF); err != nil {
		return err
	}
	thumbprint, err := signWinUSBPackage(dir)
	if err != nil {
		return err
	}
	if err := verifyWinUSBPackageINF(dir, expectedINF); err != nil {
		return errors.Join(err, removeWinUSBSigningCerts(thumbprint))
	}
	if err := runPnputilHidden(system32Path("pnputil.exe"), `/add-driver "`+inf+`" /install`); err != nil && !pnputilNoMatchingDevice(err) {
		return errors.Join(fmt.Errorf("pnputil /add-driver: %w", err), removeWinUSBSigningCerts(thumbprint))
	}
	// The new package outranks the old ones (newer DriverVer); drop them and
	// their certificates.
	for _, old := range previous {
		if err := runPnputilHidden(system32Path("pnputil.exe"), "/delete-driver "+old+" /force"); err != nil {
			log.Printf("WinUSB: removing previous package %s: %v", old, err)
		}
	}
	if err := removeWinUSBSigningCertsExcept(thumbprint); err != nil {
		log.Printf("WinUSB: removing previous signing certificates: %v", err)
	}
	return nil
}

// pnputilNoMatchingDevice reports pnputil's "added to the driver store, but
// no attached device matched" exit codes, which are fine at install time.
func pnputilNoMatchingDevice(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	switch uint32(exitErr.ExitCode()) {
	case 259, 0xE000020B: // ERROR_NO_MORE_ITEMS, ERROR_NO_SUCH_DEVINST
		return true
	}
	return false
}

// signWinUSBPackage creates and signs the catalog for the INF in dir and
// returns the (now key-less, trusted) signing certificate's thumbprint.
func signWinUSBPackage(dir string) (string, error) {
	return signLocalDriverCatalog(dir, winusbPackageINFName, aoaWinUSBCatalogName,
		winusbSignerSubject, "DrawSplay USB driver (this PC only)")
}

func verifyWinUSBPackageINF(dir string, expected []byte) error {
	got, err := os.ReadFile(filepath.Join(dir, winusbPackageINFName))
	if err != nil {
		return fmt.Errorf("read staged WinUSB INF: %w", err)
	}
	if !bytes.Equal(got, expected) {
		return errors.New("staged WinUSB INF differs from the generated in-box binding; refusing installation")
	}
	return nil
}

// A nonempty infName generates a catalog for the in-box WinUSB binding.
// IddCx uses its embedded Inf2Cat catalog covering both the INF and DLL.
func signLocalDriverCatalog(dir, infName, catName, subject, friendlyName string) (string, error) {
	if err := verifyDriverStageDir(dir); err != nil {
		return "", err
	}
	prepare := "$existing=Get-AuthenticodeSignature -FilePath $cat\n" +
		"if($existing.Status -eq 'Valid'){ 'PRESERVED_SIGNATURE'; exit 0 }\n" +
		"if($existing.Status -ne 'NotSigned'){ throw ('existing driver catalog signature is invalid: '+$existing.StatusMessage) }"
	if infName != "" {
		prepare = "$inf=Join-Path $dir '" + infName + "'\n" +
			"New-FileCatalog -Path $inf -CatalogFilePath $cat -CatalogVersion 2 | Out-Null\n" +
			"if((Test-FileCatalog -Path $inf -CatalogFilePath $cat) -ne 'Valid'){ throw 'WinUSB catalog does not match generated INF' }"
	}
	script := strings.Join([]string{
		"$ErrorActionPreference='Stop'",
		"$dir=$env:DRAWSPLAY_DRIVER_SIGN_DIR",
		"$cat=Join-Path $dir '" + catName + "'",
		prepare,
		"$cert=$null",
		"try {",
		"  $cert=New-SelfSignedCertificate -Type CodeSigningCert -Subject ('" + subject + " ('+$env:COMPUTERNAME+')') " +
			"-FriendlyName '" + friendlyName + "' -CertStoreLocation Cert:\\LocalMachine\\My " +
			"-KeyExportPolicy NonExportable -NotAfter (Get-Date).AddYears(30) -HashAlgorithm SHA256",
		"  try {",
		"    $sig=Set-AuthenticodeSignature -FilePath $cat -Certificate $cert -HashAlgorithm SHA256",
		"    if(-not $sig.SignerCertificate -or $sig.SignerCertificate.Thumbprint -ne $cert.Thumbprint){ throw ('signing failed: '+$sig.StatusMessage) }",
		"  } finally { Remove-Item -LiteralPath ('Cert:\\LocalMachine\\My\\'+$cert.Thumbprint) -DeleteKey }",
		"  foreach($name in 'Root','TrustedPublisher'){ $st=New-Object Security.Cryptography.X509Certificates.X509Store($name,'LocalMachine'); $st.Open('ReadWrite'); $st.Add([Security.Cryptography.X509Certificates.X509Certificate2]::new($cert.RawData)); $st.Close() }",
		"  $v=Get-AuthenticodeSignature -FilePath $cat",
		"  if($v.Status -ne 'Valid'){ throw ('catalog signature not trusted: '+$v.StatusMessage) }",
		"  'THUMBPRINT='+$cert.Thumbprint",
		"} catch {",
		"  if($cert){ foreach($name in 'Root','TrustedPublisher'){ $path='Cert:\\LocalMachine\\'+$name+'\\'+$cert.Thumbprint; if(Test-Path -LiteralPath $path){ Remove-Item -LiteralPath $path } } }",
		"  throw",
		"}",
	}, "\n")
	cmd := exec.Command(windowsPowerShellPath(), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "DRAWSPLAY_DRIVER_SIGN_DIR="+dir)
	hideChildWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sign %s: %w (%s)", catName, err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "PRESERVED_SIGNATURE" {
			return "", nil
		}
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "THUMBPRINT="); ok && t != "" {
			return t, nil
		}
	}
	return "", fmt.Errorf("sign %s: no certificate thumbprint in output (%s)", catName, strings.TrimSpace(string(out)))
}

// removeWinUSBSigningCerts (elevated) deletes the DrawSplay driver-signing
// certificate with thumbprint from the machine stores, or every one when
// thumbprint is "".
func removeWinUSBSigningCerts(thumbprint string) error {
	return removeDriverSigningCertsMatching("$_.Subject -like '" + winusbSignerSubject + "*' -and ('" + thumbprint + "' -eq '' -or $_.Thumbprint -eq '" + thumbprint + "')")
}

// removeWinUSBSigningCertsExcept deletes DrawSplay driver-signing
// certificates other than keep (the one signing the current package).
func removeWinUSBSigningCertsExcept(keep string) error {
	return removeDriverSigningCertsMatching("$_.Subject -like '" + winusbSignerSubject + "*' -and $_.Thumbprint -ne '" + keep + "'")
}

func removeDriverSigningCertsMatching(filter string) error {
	script := "$ErrorActionPreference='Stop'; foreach($name in 'Root','TrustedPublisher','My'){ Get-ChildItem ('Cert:\\LocalMachine\\'+$name) | " +
		"Where-Object { " + filter + " } | ForEach-Object { Remove-Item -LiteralPath $_.PSPath -DeleteKey:($name -eq 'My') } }"
	out, err := runHiddenCommand(windowsPowerShellPath(), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		return fmt.Errorf("%w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// attachedWinUSBExtras lists attached tablets (known Android vendors, not in
// accessory mode) that have no WinUSB interface yet.
func attachedWinUSBExtras() [][2]uint16 {
	var ids [][2]uint16
	seen := map[[2]uint16]bool{}
	devices, err := aoaListUSBDevices()
	if err != nil {
		return nil
	}
	for _, d := range devices {
		id := [2]uint16{d.VID, d.PID}
		if len(d.WinUSBPaths) > 0 || seen[id] || (aoaCandidate{VID: d.VID, PID: d.PID}).inAccessoryMode() {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// runWinUSBBindMode is the elevated `-winusb-bind-attached` entry point (run
// by the installed scheduled task, or via UAC for a portable host).
func runWinUSBBindMode() int {
	err := installSignedWinUSBPackage(attachedWinUSBExtras())
	writeHelperTaskResult(winusbBindResultName, err)
	if err != nil {
		log.Printf("WinUSB driver setup failed: %v", err)
		return 1
	}
	return 0
}
