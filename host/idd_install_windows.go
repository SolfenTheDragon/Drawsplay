//go:build windows

package main

// Automatic install of DrawSplay's own IddCx virtual display driver.
//
// DrawSplay bundles its virtual display driver (built from drivers/windows/idd)
// directly in the host binary via go:embed, replacing the former dependency on
// the third-party Parsec VDD driver. The driver files are extracted at setup
// time, locally catalog-signed with a per-PC certificate when unsigned, and
// installed with native Windows APIs (setupapi / newdev / pnputil).
// Signing uses in-box Windows PowerShell; no SDK or network download is needed.
//
// Extend mode creates a root-enumerated software device (Root\DrawSplayIdd);
// the UMDF/IddCx driver exposes a control interface the host opens
// (idd_windows.go) to plug the session monitor at the tablet's native
// resolution and refresh rate.
//
// Disable with -no-driver-install (the host then falls back to mirror mode).

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed driver/idd
var embeddedDriver embed.FS

const (
	iddHardwareID = `Root\DrawSplayIdd`
	iddInfName    = "DrawSplayIdd.inf"

	driverStatusPresent       = "present"
	driverStatusInstalled     = "installed"
	driverStatusPendingReboot = "installed (reboot may be required)"

	driverInstallTimeout = 7 * time.Minute
)

// driverBundled reports whether this build embeds the driver package. Local
// builds without the artifacts can use an existing Parsec VDD or mirror.
func driverBundled() bool {
	for _, name := range []string{"DrawSplayIdd.dll", iddInfName, "DrawSplayIdd.cat"} {
		data, err := embeddedDriver.ReadFile("driver/idd/" + name)
		if err != nil || len(data) == 0 {
			return false
		}
	}
	return true
}

// extractEmbeddedDriver writes the embedded driver package to dir, returning
// the full path to the INF.
func extractEmbeddedDriver(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var infPath string
	for _, name := range []string{"DrawSplayIdd.dll", iddInfName, "DrawSplayIdd.cat"} {
		data, err := embeddedDriver.ReadFile("driver/idd/" + name)
		if err != nil {
			return "", fmt.Errorf("read embedded %s: %w", name, err)
		}
		dst := filepath.Join(dir, name)
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", dst, err)
		}
		if name == iddInfName {
			infPath = dst
		}
	}
	return infPath, nil
}

// ensureVirtualDisplayDriver installs DrawSplay's virtual display driver if it
// is not already present. Best-effort: on any failure it logs and returns,
// leaving runtime selection to try existing drivers then mirror. Called once at startup.
func ensureVirtualDisplayDriver(disabled bool) {
	if disabled {
		log.Println("Virtual display driver auto-install disabled (-no-driver-install)")
		return
	}
	if _, err := iddDevicePath(); err == nil {
		return // driver present
	}
	if !driverBundled() {
		log.Println("DrawSplay driver not bundled; runtime will try an existing Parsec VDD before mirroring")
		return
	}
	if _, installed := loadInstallManifest(); installed {
		// The installer owns elevated driver setup; an installed host never
		// raises a UAC prompt on its own at launch.
		log.Println("DrawSplay virtual display driver not found; runtime will try an existing Parsec VDD before mirroring (rerun setup to install a fallback)")
		return
	}
	if isProcessElevated() {
		log.Printf("Windows virtual display setup: %s", installWindowsDisplayDriver().status)
		return
	}
	if isFirstHostBoot() && !promptInstallDriverOnFirstRun() {
		log.Println("Virtual display driver install declined; continuing in mirror mode")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("Virtual display driver install: resolve host executable: %v; mirror mode", err)
		return
	}
	if err := runElevatedAndWait(exe, "-driver-install-elevated", driverInstallTimeout); err != nil {
		log.Printf("Virtual display driver install (elevated) did not complete: %v; mirror mode", err)
		return
	}
	if _, err := iddDevicePath(); err == nil {
		log.Println("Virtual display driver installed successfully")
	} else if _, err := parsecDevicePath(); err == nil {
		log.Println("Using signed Parsec VDD fallback")
	} else {
		log.Println("Virtual display driver install did not expose the control device; a reboot may be required for extend mode")
	}
}

func logDriverInstallStatus(status string) {
	switch status {
	case driverStatusPendingReboot:
		log.Println("Virtual display driver installed but device not yet available; a Windows reboot may be required for extend mode")
	case driverStatusInstalled, driverStatusPresent:
		log.Println("Virtual display driver installed successfully")
	default:
		log.Printf("Virtual display driver install %s; continuing in mirror mode", status)
	}
}

// runDriverInstallAdminMode handles the internal elevated entry point
// (-driver-install-elevated). It installs the driver and returns true to tell
// main() to exit.
func runDriverInstallAdminMode(run bool) bool {
	if !run {
		return false
	}
	result := installWindowsDisplayDriver()
	log.Printf("Elevated virtual display driver install: %s", result.status)
	return true
}

// installIDDDriverPackage extracts the bundled driver and installs it. It must
// run elevated. It returns driverStatus* or a "failed: ..." description.
func installIDDDriverPackage() string {
	if !isProcessElevated() {
		return "failed: virtual display driver installation requires administrator approval"
	}
	if _, err := iddDevicePath(); err == nil {
		return driverStatusPresent
	}
	if !driverBundled() {
		return "skipped (driver not bundled in this build); mirror mode only"
	}
	stageDir, err := driverStageDir()
	if err != nil {
		return fmt.Sprintf("failed: staging dir: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(stageDir); err != nil {
			log.Printf("Remove temporary virtual display driver files: %v", err)
		}
	}()
	infPath, err := extractEmbeddedDriver(stageDir)
	if err != nil {
		return fmt.Sprintf("failed: extract driver: %v", err)
	}
	reboot, err := installLocallySignedIDD(iddPackageInstallOps{
		sign: func() (string, error) { return signIDDDriverPackage(stageDir) },
		stage: func() error {
			return runPnputilHidden(system32Path("pnputil.exe"), `/add-driver "`+infPath+`"`)
		},
		device: func() (bool, error) {
			return createRootDevnodeAndInstall(iddHardwareID, infPath)
		},
		removeCert: removeIDDSigningCerts,
	})
	if err != nil {
		return fmt.Sprintf("failed: %v", err)
	}
	if reboot {
		log.Println("Virtual display driver install reported a pending reboot")
	}
	// The control interface can take a moment to appear; retry briefly.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := iddDevicePath(); err == nil {
			markVDDOwned()
			return driverStatusInstalled
		}
		if time.Now().After(deadline) {
			markVDDOwned()
			return driverStatusPendingReboot
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// markVDDOwned records that DrawSplay installed the virtual display driver so
// the uninstaller can remove it without asking.
func markVDDOwned() {
	dir, err := configDir()
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, vddOwnedMarkerName), []byte("installed by DrawSplay\n"), 0o600); err != nil {
		log.Printf("Could not record virtual display driver ownership: %v", err)
	}
}

func promptInstallDriverOnFirstRun() bool {
	decision := askYesNo("DrawSplay",
		"The DrawSplay virtual display driver is not installed.\n\n"+
			"Install it now for extend-mode support?\n\n"+
			"An unsigned driver catalog will be signed on this PC. This adds a "+
			"DrawSplay certificate to this computer's Trusted Root and Trusted "+
			"Publishers stores, then deletes its private signing key. Uninstall "+
			"removes the driver and certificate. Secure Boot and signature "+
			"enforcement are not changed. If this driver fails, setup uses an "+
			"existing Parsec VDD or downloads a verified fallback only if needed."+
			"\n\nRequires administrator approval.")
	if err := persistNoDriverInstallPreference(!decision); err != nil {
		log.Printf("Saving driver install preference failed (%v)", err)
	}
	return decision
}

func isFirstHostBoot() bool {
	path, err := configPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return os.IsNotExist(err)
}

func persistNoDriverInstallPreference(skipInstall bool) error {
	cfg := loadSettings()
	cfg.NoDriverInstall = skipInstall
	return saveSettings(cfg)
}

func truncateForLog(s string) string {
	const max = 300
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// ---- Native root devnode creation (setupapi + newdev) ----

const (
	dicdGenerateID    = 0x00000001 // DICD_GENERATE_ID
	spdrpHardwareID   = 0x00000001 // SPDRP_HARDWAREID
	difRegisterDevice = 0x00000019 // DIF_REGISTERDEVICE
	difRemove         = 0x00000005 // DIF_REMOVE
	installFlagForce  = 0x00000001 // INSTALLFLAG_FORCE
	maxClassNameLen   = 32

	digcfPresent    = 0x00000002 // DIGCF_PRESENT
	digcfAllClasses = 0x00000004 // DIGCF_ALLCLASSES
)

// spDevInfoData mirrors SP_DEVINFO_DATA (x64: 32 bytes).
type spDevInfoData struct {
	cbSize    uint32
	classGUID windows.GUID
	devInst   uint32
	reserved  uintptr
}

var (
	setupapi = windows.NewLazySystemDLL("setupapi.dll")
	newdev   = windows.NewLazySystemDLL("newdev.dll")

	procSetupDiGetINFClassW              = setupapi.NewProc("SetupDiGetINFClassW")
	procSetupDiCreateDeviceInfoList      = setupapi.NewProc("SetupDiCreateDeviceInfoList")
	procSetupDiCreateDeviceInfoW         = setupapi.NewProc("SetupDiCreateDeviceInfoW")
	procSetupDiSetDeviceRegistryProperty = setupapi.NewProc("SetupDiSetDeviceRegistryPropertyW")
	procSetupDiCallClassInstaller        = setupapi.NewProc("SetupDiCallClassInstaller")
	procSetupDiDestroyDeviceInfoList     = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
	procSetupDiGetClassDevsW             = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInfo            = setupapi.NewProc("SetupDiEnumDeviceInfo")
	procSetupDiGetDeviceRegistryProperty = setupapi.NewProc("SetupDiGetDeviceRegistryPropertyW")

	procUpdateDriverForPlugAndPlayDevicesW = newdev.NewProc("UpdateDriverForPlugAndPlayDevicesW")
)

const invalidHandleValue = ^uintptr(0)

// createRootDevnodeAndInstall creates (if needed) the root-enumerated software
// device for hardwareID and installs the driver from infPath. Returns whether
// a reboot is required.
func createRootDevnodeAndInstall(hardwareID, infPath string) (bool, error) {
	infPtr, err := windows.UTF16PtrFromString(infPath)
	if err != nil {
		return false, err
	}
	var classGUID windows.GUID
	className := make([]uint16, maxClassNameLen)
	r, _, callErr := procSetupDiGetINFClassW.Call(
		uintptr(unsafe.Pointer(infPtr)),
		uintptr(unsafe.Pointer(&classGUID)),
		uintptr(unsafe.Pointer(&className[0])),
		uintptr(maxClassNameLen),
		0,
	)
	if r == 0 {
		return false, fmt.Errorf("SetupDiGetINFClass: %v", callErr)
	}

	hDevInfo, _, callErr := procSetupDiCreateDeviceInfoList.Call(
		uintptr(unsafe.Pointer(&classGUID)), 0)
	if hDevInfo == invalidHandleValue {
		return false, fmt.Errorf("SetupDiCreateDeviceInfoList: %v", callErr)
	}
	defer procSetupDiDestroyDeviceInfoList.Call(hDevInfo)

	var devInfo spDevInfoData
	devInfo.cbSize = uint32(unsafe.Sizeof(devInfo))
	r, _, callErr = procSetupDiCreateDeviceInfoW.Call(
		hDevInfo,
		uintptr(unsafe.Pointer(&className[0])),
		uintptr(unsafe.Pointer(&classGUID)),
		0, 0,
		uintptr(dicdGenerateID),
		uintptr(unsafe.Pointer(&devInfo)),
	)
	if r == 0 {
		return false, fmt.Errorf("SetupDiCreateDeviceInfo: %v", callErr)
	}

	// SPDRP_HARDWAREID is a REG_MULTI_SZ: the id plus a double NUL terminator.
	hwid, err := windows.UTF16FromString(hardwareID)
	if err != nil {
		return false, err
	}
	hwid = append(hwid, 0) // extra NUL -> multi-sz terminator
	r, _, callErr = procSetupDiSetDeviceRegistryProperty.Call(
		hDevInfo,
		uintptr(unsafe.Pointer(&devInfo)),
		uintptr(spdrpHardwareID),
		uintptr(unsafe.Pointer(&hwid[0])),
		uintptr(len(hwid)*2),
	)
	if r == 0 {
		return false, fmt.Errorf("SetupDiSetDeviceRegistryProperty(HardwareID): %v", callErr)
	}

	r, _, callErr = procSetupDiCallClassInstaller.Call(
		uintptr(difRegisterDevice), hDevInfo, uintptr(unsafe.Pointer(&devInfo)))
	if r == 0 {
		return false, fmt.Errorf("SetupDiCallClassInstaller(REGISTERDEVICE): %v", callErr)
	}

	// Install the matching driver onto the freshly registered devnode.
	hwidPtr, _ := windows.UTF16PtrFromString(hardwareID)
	var reboot int32
	r, _, callErr = procUpdateDriverForPlugAndPlayDevicesW.Call(
		0,
		uintptr(unsafe.Pointer(hwidPtr)),
		uintptr(unsafe.Pointer(infPtr)),
		uintptr(installFlagForce),
		uintptr(unsafe.Pointer(&reboot)),
	)
	if r == 0 {
		return false, fmt.Errorf("UpdateDriverForPlugAndPlayDevices: %v", callErr)
	}
	return reboot != 0, nil
}

// removeIDDDriver removes the root devnode(s) matching iddHardwareID. Best
// effort; used by the uninstaller.
func removeIDDDriver() error {
	hwidWant, err := windows.UTF16FromString(iddHardwareID)
	if err != nil {
		return err
	}
	hDevInfo, _, callErr := procSetupDiGetClassDevsW.Call(
		0, 0, 0, uintptr(digcfAllClasses|digcfPresent))
	if hDevInfo == invalidHandleValue {
		return fmt.Errorf("SetupDiGetClassDevs: %v", callErr)
	}
	defer procSetupDiDestroyDeviceInfoList.Call(hDevInfo)

	var removed int
	buf := make([]byte, 4096)
	for i := uint32(0); ; i++ {
		var devInfo spDevInfoData
		devInfo.cbSize = uint32(unsafe.Sizeof(devInfo))
		r, _, _ := procSetupDiEnumDeviceInfo.Call(
			hDevInfo, uintptr(i), uintptr(unsafe.Pointer(&devInfo)))
		if r == 0 {
			break // ERROR_NO_MORE_ITEMS
		}
		var regType, reqSize uint32
		r, _, _ = procSetupDiGetDeviceRegistryProperty.Call(
			hDevInfo, uintptr(unsafe.Pointer(&devInfo)), uintptr(spdrpHardwareID),
			uintptr(unsafe.Pointer(&regType)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
			uintptr(unsafe.Pointer(&reqSize)))
		if r == 0 {
			continue
		}
		if !multiSzHasID(buf, hwidWant) {
			continue
		}
		if r, _, _ := procSetupDiCallClassInstaller.Call(
			uintptr(difRemove), hDevInfo, uintptr(unsafe.Pointer(&devInfo))); r != 0 {
			removed++
		}
	}
	if removed == 0 {
		return fmt.Errorf("no %s devnode found", iddHardwareID)
	}
	return nil
}

// multiSzHasID reports whether the REG_MULTI_SZ in buf contains the id want
// (case-insensitive), interpreting buf as UTF-16.
func multiSzHasID(buf []byte, want []uint16) bool {
	u16 := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[0])), len(buf)/2)
	wantStr := windows.UTF16ToString(want)
	start := 0
	for i := 0; i < len(u16); i++ {
		if u16[i] == 0 {
			if i > start {
				if equalFoldUTF16(u16[start:i], wantStr) {
					return true
				}
			}
			if i == start { // double NUL -> end
				break
			}
			start = i + 1
		}
	}
	return false
}

func equalFoldUTF16(s []uint16, want string) bool {
	got := windows.UTF16ToString(s)
	return equalFoldASCII(got, want)
}
