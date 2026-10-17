//go:build windows

package main

// Windows installer and uninstaller.
//
// `DrawSplay.exe -install` (or answering Yes when DrawSplay.exe is launched
// uninstalled) asks for administrator approval exactly once and then:
//
//   - copies the host to %ProgramFiles%\DrawSplay\DrawSplay.exe plus the
//     uninstaller DrawSplay-Uninstall.exe (admin-only writable, so the
//     elevated helper task below cannot be hijacked by user-level code);
//   - installs DrawSplay's bundled IddCx virtual display driver (extend mode)
//     unless -no-driver-install;
//   - attempts WinUSB for the AOA accessory IDs and attached tablets;
//   - adds an Apps & Features entry.
//
// Per-user state lives in Roaming (%APPDATA%): install.json and settings in
// %APPDATA%\DrawSplay, Start Menu shortcuts, and the HKCU Run autostart that
// launches the host in the tray at login.
//
// DrawSplay-Uninstall.exe (or -uninstall) reverses all of it after one
// approval; it can keep or delete the settings in %APPDATA%\DrawSplay. The
// bundled virtual display driver is removed when DrawSplay installed it;
// WinUSB bindings are removed too.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	installAppName        = "DrawSplay"
	installHostExeName    = "DrawSplay.exe"
	installUninstallName  = "DrawSplay-Uninstall.exe"
	uninstallRegKeyPath   = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\DrawSplay`
	uninstallRelaunchEnv  = "DRAWSPLAY_UNINSTALL_RELAUNCHED"
	installElevateTimeout = 15 * time.Minute
	portableMarkerName    = "portable-mode"
)

// installerOptions carries the installer-related command-line flags.
type installerOptions struct {
	Install           bool
	Uninstall         bool
	Quiet             bool
	KeepSettings      bool
	RemoveShared      bool
	DesktopShortcut   bool
	Autostart         bool // start at sign-in without asking
	NoAutostart       bool // do not start at sign-in, without asking
	SkipVDD           bool
	InstallElevated   string // internal: elevated install phase, result path
	WinUSBBind        bool   // internal: elevated WinUSB driver setup (scheduled task / UAC)
	UninstallElevated string // internal: elevated uninstall phase, result path
	NoArgs            bool
}

type uninstallResult struct {
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Removed  []string `json:"removed,omitempty"`
}

// runInstallerMode handles install/uninstall invocations. handled is false
// when the process should continue as a normal host.
func runInstallerMode(o installerOptions) (handled bool, code int) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	switch {
	case o.InstallElevated != "":
		return true, runInstallElevated(o.InstallElevated, o.SkipVDD)
	case o.WinUSBBind:
		return true, runWinUSBBindMode()
	case o.UninstallElevated != "":
		return true, runUninstallElevated(o.UninstallElevated)
	case o.Uninstall || isUninstallerExeName(exe):
		return true, runUninstall(exe, o)
	case o.Install, o.NoArgs && isInstallerExeName(exe):
		return true, runInstall(exe, o)
	case o.NoArgs && shouldOfferInstall(exe):
		if askYesNo("Install DrawSplay",
			"DrawSplay is not installed on this PC.\n\n"+
				"Install it now? Windows will ask for administrator approval once; after that "+
				"DrawSplay starts in the system tray at login without further prompts.\n\n"+
				"Choose No to run it this time without installing.") {
			return true, runInstall(exe, o)
		}
		if dir, err := configDir(); err == nil {
			_ = os.WriteFile(filepath.Join(dir, portableMarkerName), []byte("declined install offer\n"), 0o600)
		}
	}
	return false, 0
}

func defaultInstallDir() string {
	if dir, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0); err == nil && dir != "" {
		return filepath.Join(dir, installAppName)
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		return filepath.Join(pf, installAppName)
	}
	return filepath.Join(`C:\Program Files`, installAppName)
}

func shouldOfferInstall(exe string) bool {
	if os.Getenv(bgChildEnv) != "" {
		return false
	}
	if _, installed := loadInstallManifest(); installed {
		return false
	}
	if pathInsideDir(exe, defaultInstallDir()) {
		return false
	}
	if dir, err := userStateDir(); err == nil && fileExists(filepath.Join(dir, portableMarkerName)) {
		return false
	}
	return true
}

func isProcessElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// installerReport prints to the console (if any) and, unless quiet, shows a
// message box.
func installerReport(quiet bool, title, text string, show func(title, text string)) {
	fmt.Println(title + ": " + text)
	if !quiet {
		show(title, text)
	}
}

// ---------------------------------------------------------------- install

func runInstall(exe string, o installerOptions) int {
	attachParentConsole()
	if loadSettings().NoDriverInstall {
		o.SkipVDD = true
	}
	previous, _ := loadInstallManifest()
	wantDesktop := o.DesktopShortcut || (o.Quiet && previous.DesktopShortcut != "")
	if !o.Quiet && !o.DesktopShortcut {
		wantDesktop = askYesNo("Install DrawSplay", "Add a DrawSplay shortcut to the desktop?")
	}
	wantAutostart := installWantsAutostart(o, previous.HostExe != "")
	if !o.Quiet && !o.Autostart && !o.NoAutostart {
		wantAutostart = askYesNo("Install DrawSplay",
			"Start DrawSplay automatically in the system tray when you sign in to Windows?\n\n"+
				"You can change this later on the DrawSplay settings page.")
	}
	dir := defaultInstallDir()
	fmt.Printf("Installing DrawSplay to %s\n", dir)

	// Release the installed binaries if an older copy is running.
	stopProcessesInDir(dir)

	resultPath := filepath.Join(os.TempDir(), fmt.Sprintf("drawsplay-install-%d.json", os.Getpid()))
	_ = os.Remove(resultPath)
	defer os.Remove(resultPath)

	var elevErr error
	if isProcessElevated() {
		if runInstallElevated(resultPath, o.SkipVDD) != 0 {
			elevErr = errors.New("elevated install phase failed")
		}
	} else {
		args := fmt.Sprintf(`-install-elevated "%s"`, resultPath)
		if o.SkipVDD {
			args += " -no-driver-install"
		}
		elevErr = runElevatedAndWait(exe, args, installElevateTimeout)
	}
	m, ok, readErr := readInstallManifest(resultPath)
	if !ok || readErr != nil || m.HostExe == "" || !fileExists(m.HostExe) {
		detail := "administrator approval was not granted or setup failed"
		if elevErr != nil {
			detail = elevErr.Error()
		}
		if readErr != nil {
			detail += "; " + readErr.Error()
		}
		if len(m.Warnings) > 0 {
			detail += "\n\n" + strings.Join(m.Warnings, "\n")
		}
		installerReport(o.Quiet, "DrawSplay setup failed", detail, showError)
		return 1
	}

	stateDir, err := configDir()
	if err != nil {
		installerReport(o.Quiet, "DrawSplay setup failed", fmt.Sprintf("cannot create %%APPDATA%%\\DrawSplay: %v", err), showError)
		return 1
	}
	for key, result := range m.WinUSB {
		if err := recordWinUSBAttempt(stateDir, key, result, m.InstalledAt); err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("record WinUSB result for %s: %v", key, err))
		}
	}
	if m.VDDDriver == driverStatusInstalled || m.VDDDriver == driverStatusPendingReboot || m.VDDOwned {
		markVDDOwned()
	}
	m.VDDOwned = m.VDDOwned || fileExists(filepath.Join(stateDir, vddOwnedMarkerName))
	if wantAutostart {
		if err := installAutostartCommand(m.HostExe); err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("login autostart: %v", err))
		}
	} else if err := uninstallAutostart(); err != nil {
		m.Warnings = append(m.Warnings, fmt.Sprintf("remove login autostart: %v", err))
	}
	if err := createStartMenuShortcuts(m.HostExe, m.UninstallerExe); err != nil {
		m.Warnings = append(m.Warnings, fmt.Sprintf("Start Menu shortcuts: %v", err))
	}
	if lnk, err := desktopShortcutPath(); err != nil {
		if wantDesktop {
			m.Warnings = append(m.Warnings, fmt.Sprintf("desktop shortcut: %v", err))
		}
	} else if wantDesktop {
		if err := createShortcut(lnk, m.HostExe, "Start DrawSplay in the system tray"); err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("desktop shortcut: %v", err))
		} else {
			m.DesktopShortcut = lnk
		}
	} else if previous.DesktopShortcut != "" {
		_ = os.Remove(previous.DesktopShortcut)
	}
	_ = os.Remove(filepath.Join(stateDir, portableMarkerName))
	if err := writeJSONAtomic(filepath.Join(stateDir, installManifestName), m); err != nil {
		installerReport(o.Quiet, "DrawSplay setup failed", fmt.Sprintf("write install manifest: %v", err), showError)
		return 1
	}

	launch := exec.Command(m.HostExe)
	launch.Dir = m.InstallDir
	if err := launch.Start(); err != nil {
		m.Warnings = append(m.Warnings, fmt.Sprintf("start DrawSplay: %v", err))
	} else {
		_ = launch.Process.Release()
	}

	startup := "It starts automatically when you sign in."
	if !wantAutostart {
		startup = "It does not start at sign-in (enable this on the settings page)."
	}
	summary := fmt.Sprintf("DrawSplay is installed in %s and is running in the system tray.\n"+
		"%s Settings are stored in %s.\n\n"+
		"Virtual display driver: %s\nWinUSB: %s",
		m.InstallDir, startup, stateDir, valueOr(m.VDDDriver, "unknown"), summarizeWinUSB(m.WinUSB))
	if len(m.Warnings) > 0 {
		summary += "\n\nWarnings:\n" + strings.Join(m.Warnings, "\n")
		installerReport(o.Quiet, "DrawSplay installed", summary, showWarning)
	} else {
		installerReport(o.Quiet, "DrawSplay installed", summary, showInfo)
	}
	return 0
}

// runInstallElevated is the administrator phase of -install. It writes its
// outcome as an installManifest to resultPath.
func runInstallElevated(resultPath string, skipVDD bool) int {
	dir := defaultInstallDir()
	m := installManifest{
		SchemaVersion:  installManifestVersion,
		InstallDir:     dir,
		HostExe:        filepath.Join(dir, installHostExeName),
		UninstallerExe: filepath.Join(dir, installUninstallName),
		InstalledAt:    time.Now().UTC(),
		WinUSB:         map[string]string{},
	}
	fail := func(format string, args ...any) int {
		m.HostExe = ""
		m.Warnings = append(m.Warnings, fmt.Sprintf(format, args...))
		_ = writeJSONAtomic(resultPath, m)
		return 1
	}
	if !isProcessElevated() {
		return fail("install phase is not running as administrator")
	}
	src, err := os.Executable()
	if err != nil {
		return fail("resolve installer executable: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail("create %s: %v", dir, err)
	}
	stopProcessesInDir(dir)
	for _, dst := range []string{m.HostExe, m.UninstallerExe} {
		if err := copyFileReplacing(src, dst); err != nil {
			return fail("copy %s: %v", dst, err)
		}
	}

	// The non-elevated host cannot write runtime downloads into Program
	// Files, so install the newest compatible ffmpeg now. It is preferred
	// over any ffmpeg on PATH (often an old build lacking the GPU capture
	// paths); each install or repair updates it.
	if err := installFFmpeg(filepath.Join(dir, "runtime", "ffmpeg")); err != nil {
		if _, pathErr := exec.LookPath("ffmpeg"); pathErr == nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("ffmpeg download: %v (using the ffmpeg on PATH instead; rerun setup to retry)", err))
		} else {
			m.Warnings = append(m.Warnings, fmt.Sprintf("ffmpeg download: %v (install ffmpeg on PATH or rerun setup)", err))
		}
	}

	if _, err := iddDevicePath(); err == nil {
		m.VDDDriver = driverStatusPresent
	} else if skipVDD {
		m.VDDDriver = "skipped (-no-driver-install); mirror mode only"
	} else {
		m.VDDDriver = installIDDDriverPackage()
	}
	if m.VDDDriver == driverStatusInstalled || m.VDDDriver == driverStatusPendingReboot || m.VDDDriver == driverStatusPresent {
		m.VDDOwned = true
	}

	extras := attachedWinUSBExtras()
	winusbResult := "installed"
	if err := installSignedWinUSBPackage(extras); err != nil {
		winusbResult = "failed: " + err.Error()
	}
	for _, id := range append(aoaAccessoryIDs(), extras...) {
		m.WinUSB[fmt.Sprintf("%04x:%04x", id[0], id[1])] = winusbResult
	}
	if err := registerHelperTask(winusbBindTaskName, helperTaskXML(m.HostExe, "",
		"Installs DrawSplay's signed WinUSB driver for a newly attached tablet without a UAC prompt. Started on demand by the DrawSplay host.",
		"-winusb-bind-attached")); err != nil {
		m.Warnings = append(m.Warnings, fmt.Sprintf("WinUSB helper task: %v", err))
	} else {
		m.WinUSBTask = winusbBindTaskName
	}

	if err := writeUninstallRegistryEntry(m); err != nil {
		m.Warnings = append(m.Warnings, fmt.Sprintf("Apps & Features entry: %v", err))
	}
	if err := writeJSONAtomic(resultPath, m); err != nil {
		log.Printf("write install result: %v", err)
		return 1
	}
	return 0
}

// aoaAccessoryIDs lists the Android accessory-mode identities.
func aoaAccessoryIDs() [][2]uint16 {
	var ids [][2]uint16
	for pid := uint16(aoaPIDFirst); pid <= aoaPIDLast; pid++ {
		ids = append(ids, [2]uint16{aoaGoogleVID, pid})
	}
	return ids
}

func runPnputilHidden(pnputil, args string) error {
	cmd := exec.Command(pnputil)
	cmd.SysProcAttr = nil
	hideChildWindow(cmd)
	cmd.SysProcAttr.CmdLine = `"` + pnputil + `" ` + args
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 3010 {
		return nil // installed; reboot required
	}
	if err != nil {
		return fmt.Errorf("%w (%s)", err, lastLine(string(out)))
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func copyFileReplacing(src, dst string) error {
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// helperTaskXML builds a trigger-less, on-demand task that runs the installed
// host elevated (highest available) as userSID ("" = the current user).
func helperTaskXML(hostExe, userSID, description, args string) string {
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>` + esc(description) + `</Description>
  </RegistrationInfo>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(userSID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <ExecutionTimeLimit>PT2M</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(hostExe) + `</Command>
      <Arguments>` + esc(args) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// registerHelperTask registers taskXML as name; an empty <UserId> is filled
// with the current (installing) user's SID.
func registerHelperTask(name, taskXML string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("current user SID: %w", err)
	}
	taskXML = strings.Replace(taskXML, "<UserId></UserId>", "<UserId>"+user.User.Sid.String()+"</UserId>", 1)
	f, err := os.CreateTemp("", "drawsplay-task-*.xml")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	// Task Scheduler expects UTF-16 for an encoding="UTF-16" declaration.
	u := utf16.Encode([]rune(taskXML))
	buf := make([]byte, 2+2*len(u))
	buf[0], buf[1] = 0xFF, 0xFE
	for i, r := range u {
		binary.LittleEndian.PutUint16(buf[2+2*i:], r)
	}
	_, err = f.Write(buf)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if out, err := runHiddenCommand(schtasksPath(), "/create", "/tn", name, "/xml", path, "/f"); err != nil {
		return fmt.Errorf("schtasks /create: %w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

func writeUninstallRegistryEntry(m installManifest) error {
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallRegKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	var sizeKB uint32
	if fi, err := os.Stat(m.HostExe); err == nil {
		sizeKB = uint32(fi.Size() * 2 / 1024)
	}
	strs := map[string]string{
		"DisplayName":          installAppName,
		"DisplayIcon":          m.HostExe,
		"Publisher":            installAppName,
		"InstallLocation":      m.InstallDir,
		"UninstallString":      `"` + m.UninstallerExe + `"`,
		"QuietUninstallString": `"` + m.UninstallerExe + `" -quiet`,
		"InstallDate":          m.InstalledAt.Local().Format("20060102"),
	}
	for name, value := range strs {
		if err := key.SetStringValue(name, value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, value := range map[string]uint32{"NoModify": 1, "NoRepair": 1, "EstimatedSize": sizeKB} {
		if err := key.SetDWordValue(name, value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func startMenuDir() (string, error) {
	base, err := os.UserConfigDir() // %APPDATA% (Roaming)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", installAppName), nil
}

func createStartMenuShortcuts(hostExe, uninstallerExe string) error {
	dir, err := startMenuDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := createShortcut(filepath.Join(dir, "DrawSplay.lnk"), hostExe, "Start DrawSplay in the system tray"); err != nil {
		return err
	}
	return createShortcut(filepath.Join(dir, "Uninstall DrawSplay.lnk"), uninstallerExe, "Uninstall DrawSplay")
}

// desktopShortcutPath is the current user's desktop DrawSplay.lnk (honours
// Desktop folder redirection, e.g. OneDrive).
func desktopShortcutPath() (string, error) {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, installAppName+".lnk"), nil
}

func summarizeWinUSB(results map[string]string) string {
	if len(results) == 0 {
		return "nothing to install"
	}
	ok, failed := 0, 0
	for _, r := range results {
		if r == "installed" {
			ok++
		} else {
			failed++
		}
	}
	if failed == 0 {
		return fmt.Sprintf("installed for %d device IDs", ok)
	}
	return fmt.Sprintf("%d installed, %d not installed (see the install log; reconnect the tablet or rerun the installer)", ok, failed)
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// -------------------------------------------------------------- uninstall

func runUninstall(exe string, o installerOptions) int {
	attachParentConsole()
	m, installed := loadInstallManifest()
	dir := defaultInstallDir()
	if installed && m.InstallDir != "" {
		dir = m.InstallDir
	}

	// The uninstaller cannot delete the folder it runs from: relaunch from a
	// temp copy and exit.
	if os.Getenv(uninstallRelaunchEnv) == "" && pathInsideDir(exe, dir) {
		tmp := filepath.Join(os.TempDir(), fmt.Sprintf("DrawSplay-Uninstall-%d.exe", os.Getpid()))
		if err := copyFileReplacing(exe, tmp); err != nil {
			installerReport(o.Quiet, "DrawSplay uninstall failed", fmt.Sprintf("prepare uninstaller: %v", err), showError)
			return 1
		}
		cmd := exec.Command(tmp, os.Args[1:]...)
		cmd.Env = append(os.Environ(), uninstallRelaunchEnv+"=1")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			installerReport(o.Quiet, "DrawSplay uninstall failed", fmt.Sprintf("start uninstaller: %v", err), showError)
			return 1
		}
		_ = cmd.Process.Release()
		return 0
	}
	if os.Getenv(uninstallRelaunchEnv) != "" {
		defer scheduleSelfDelete(exe)
	}

	if !o.Quiet && !askYesNo("Uninstall DrawSplay", "Remove DrawSplay from this PC?") {
		return 1
	}
	plan := planUninstallDeps(m)
	removeShared := o.RemoveShared
	if !o.Quiet && len(plan.sharedPrompt) > 0 {
		removeShared = askYesNo("Uninstall DrawSplay",
			"These components were used by DrawSplay but may also be used by other software:\n\n"+
				strings.Join(plan.sharedPrompt, "\n")+
				"\n\nRemove them as well?")
	}
	req := plan.request(removeShared)
	keep := o.KeepSettings
	if !o.Quiet && !keep {
		keep = askYesNo("Uninstall DrawSplay", "Keep your DrawSplay settings and logs (stored in %APPDATA%\\DrawSplay) for a later reinstall?")
	}

	stopProcessesInDir(dir)

	resultPath := filepath.Join(os.TempDir(), fmt.Sprintf("drawsplay-uninstall-%d.json", os.Getpid()))
	_ = os.Remove(resultPath)
	defer os.Remove(resultPath)
	reqPath := uninstallRequestPath(resultPath)
	defer os.Remove(reqPath)
	var problems []string
	if err := writeJSONAtomic(reqPath, req); err != nil {
		problems = append(problems, fmt.Sprintf("write dependency choices: %v", err))
	}
	if isProcessElevated() {
		runUninstallElevated(resultPath)
	} else if err := runElevatedAndWait(exe, fmt.Sprintf(`-uninstall-elevated "%s"`, resultPath), installElevateTimeout); err != nil {
		problems = append(problems, fmt.Sprintf("administrator phase: %v", err))
	}
	var res uninstallResult
	if data, err := os.ReadFile(resultPath); err == nil {
		if err := json.Unmarshal(data, &res); err != nil {
			problems = append(problems, fmt.Sprintf("read uninstall result: %v", err))
		}
	} else if len(problems) == 0 {
		problems = append(problems, "administrator phase reported no result")
	}
	problems = append(problems, res.Errors...)
	if len(problems) > 0 && fileExists(filepath.Join(dir, installHostExeName)) {
		installerReport(o.Quiet, "DrawSplay uninstall failed", strings.Join(problems, "\n")+"\n\nNothing was removed; run the uninstaller again.", showError)
		return 1
	}
	problems = append(problems, res.Warnings...)
	vddRemoved := false
	for _, r := range res.Removed {
		if r == "Parsec Virtual Display Driver" {
			vddRemoved = true
		}
	}
	removeUserDependencyCaches()

	if err := uninstallAutostart(); err != nil {
		problems = append(problems, fmt.Sprintf("login autostart: %v", err))
	}
	if smDir, err := startMenuDir(); err == nil {
		if err := os.RemoveAll(smDir); err != nil {
			problems = append(problems, fmt.Sprintf("Start Menu shortcuts: %v", err))
		}
	}
	if m.DesktopShortcut != "" {
		if err := os.Remove(m.DesktopShortcut); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, fmt.Sprintf("desktop shortcut: %v", err))
		}
	}
	if stateDir, err := userStateDir(); err == nil {
		if keep {
			for _, name := range []string{installManifestName, winusbAttemptsName, portableMarkerName} {
				_ = os.Remove(filepath.Join(stateDir, name))
			}
			if vddRemoved {
				_ = os.Remove(filepath.Join(stateDir, vddOwnedMarkerName))
			}
		} else if err := os.RemoveAll(stateDir); err != nil {
			problems = append(problems, fmt.Sprintf("remove %s: %v", stateDir, err))
		}
	}

	msg := "DrawSplay was removed."
	if len(res.Removed) > 0 {
		msg += "\n\nAlso removed:\n• " + strings.Join(res.Removed, "\n• ")
	}
	var kept []string
	if plan.VDDPresent && !vddRemoved {
		kept = append(kept, "Parsec Virtual Display Driver")
	}
	if len(plan.ZadigINFs) > 0 && !req.RemoveZadig {
		kept = append(kept, "Zadig WinUSB binding ("+strings.Join(plan.ZadigINFs, ", ")+")")
	}
	if len(kept) > 0 {
		msg += "\n\nLeft installed (shared with other software):\n• " + strings.Join(kept, "\n• ")
	}
	if vddRemoved {
		msg += "\n\nA Windows restart may be needed to finish removing the virtual display driver."
	}
	if keep {
		msg += "\n\nYour settings were kept in %APPDATA%\\DrawSplay."
	}
	if len(problems) > 0 {
		installerReport(o.Quiet, "DrawSplay uninstalled with warnings", msg+"\n\n"+strings.Join(problems, "\n"), showWarning)
		return 0
	}
	installerReport(o.Quiet, "DrawSplay uninstalled", msg, showInfo)
	return 0
}

// runUninstallElevated is the administrator phase of uninstall.
func runUninstallElevated(resultPath string) int {
	var res uninstallResult
	dir := defaultInstallDir()
	stopProcessesInDir(dir)
	for _, task := range []string{winusbBindTaskName} {
		if out, err := runHiddenCommand(schtasksPath(), "/delete", "/tn", task, "/f"); err != nil {
			if !strings.Contains(strings.ToLower(out), "cannot find") && !strings.Contains(strings.ToLower(out), "does not exist") {
				res.Errors = append(res.Errors, fmt.Sprintf("delete scheduled task %s: %v (%s)", task, err, strings.TrimSpace(out)))
			}
		}
	}
	// Remove the task folder too (ignore failure: it may hold other tasks).
	_, _ = runHiddenCommand(windowsPowerShellPath(), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command",
		"$s=New-Object -ComObject Schedule.Service; $s.Connect(); try { $s.GetFolder('\\').DeleteFolder('DrawSplay',0) } catch {}")
	if err := registry.DeleteKey(registry.LOCAL_MACHINE, uninstallRegKeyPath); err != nil && !errors.Is(err, registry.ErrNotExist) {
		res.Errors = append(res.Errors, fmt.Sprintf("remove Apps & Features entry: %v", err))
	}
	var rmErr error
	for i := 0; i < 20; i++ {
		if rmErr = os.RemoveAll(dir); rmErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if rmErr != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("remove %s: %v", dir, rmErr))
	}
	var req uninstallRequest
	if data, err := os.ReadFile(uninstallRequestPath(resultPath)); err == nil {
		if err := json.Unmarshal(data, &req); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("read dependency choices: %v", err))
		}
	}
	removeUninstallDeps(req, &res)
	if err := writeJSONAtomic(resultPath, res); err != nil {
		return 1
	}
	if len(res.Errors) > 0 {
		return 1
	}
	return 0
}

// scheduleSelfDelete removes the temporary uninstaller copy after it exits.
func scheduleSelfDelete(exe string) {
	q := "'" + strings.ReplaceAll(exe, "'", "''") + "'"
	script := "for($i=0;$i -lt 300;$i++){Remove-Item -LiteralPath " + q + " -Force -ErrorAction SilentlyContinue; if(-not (Test-Path -LiteralPath " + q + ")){break}; Start-Sleep -Seconds 1}"
	cmd := exec.Command(windowsPowerShellPath(), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	hideChildWindow(cmd)
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}

// stopProcessesInDir terminates host processes whose image lives in dir
// (other than this process), so their files can be replaced or removed.
func stopProcessesInDir(dir string) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	self := uint32(os.Getpid())
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == self || entry.ProcessID == 0 {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, entry.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil && pathInsideDir(windows.UTF16ToString(buf[:n]), dir) {
			if windows.TerminateProcess(h, 0) == nil {
				_, _ = windows.WaitForSingleObject(h, 5000)
			}
		}
		windows.CloseHandle(h)
	}
}

// installWantsAutostart is the sign-in autostart choice when not asking: the
// explicit flag, else the current state on a reinstall, else enabled.
func installWantsAutostart(o installerOptions, reinstall bool) bool {
	switch {
	case o.NoAutostart:
		return false
	case o.Autostart:
		return true
	case reinstall:
		enabled, err := autostartEnabled()
		return err != nil || enabled
	}
	return true
}

// installAutostartCommand points the HKCU Run entry at exe (tray mode is the
// Windows default, so no extra flags are needed).
func installAutostartCommand(exe string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening Run key: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(runValueName, fmt.Sprintf(`"%s" -background`, exe)); err != nil {
		return fmt.Errorf("writing Run entry: %w", err)
	}
	return nil
}

// handOffToRunningInstance opens the settings page of an already running
// host instead of starting a second tray instance.
func handOffToRunningInstance(background bool) bool {
	if !background || os.Getenv(bgChildEnv) != "" || !anotherInstanceRunning() {
		return false
	}
	openSettingsWebUI()
	return true
}
