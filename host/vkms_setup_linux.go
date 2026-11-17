//go:build linux

package main

// Automatic setup of the X11 virtual display: loads the vkms kernel module
// when an X11 session has no virtual RandR output. It runs when the host
// starts and whenever a tablet is newly connected. Loading a module needs
// root: the host uses root or passwordless sudo, and when a tablet connects it
// may show one polkit password prompt. It never prompts at login. See vkmsAutoloadEnv
// for when it is skipped.

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// vkmsRetryCooldown keeps a failed attempt from repeating on every
	// reconnect.
	vkmsRetryCooldown = 2 * time.Minute
	vkmsOutputWait    = 5 * time.Second
)

// vkmsSettleWatch is how long the logs are watched after X adds vkms, while
// the desktop may switch its output on by itself. A variable for tests.
var vkmsSettleWatch = 3 * time.Second

// vkmsAutoloadEnv controls loading vkms into a running X session. vkms is
// the primary virtual display and loading it is on by default; the spare
// HDMI connector is the fallback. Because on kernels such as 7.0 (Ubuntu OEM)
// the X server's modesetting driver trips a kernel WARNING in
// vkms_prepare_fb (dma_buf_vmap) when it first sets a mode on the new device,
// vkms is guarded (x11_display_guard_linux.go): a failure in the kernel log
// or an interrupted setup turns it off for that kernel, and the fallback
// takes over. "0" turns loading off for good; "1" additionally overrides the
// guard for a user who has verified vkms on their kernel.
const vkmsAutoloadEnv = "DRAWSPLAY_X11_VKMS_AUTOLOAD"

func vkmsAutoloadEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(vkmsAutoloadEnv))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// vkmsForcedByUser reports an explicit opt-in that overrides the guard.
func vkmsForcedByUser() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(vkmsAutoloadEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

var errVkmsPromptDeclined = errors.New("the password prompt was dismissed")

// errNeedsRoot: a root step could not run without a prompt and prompting was
// not allowed. Wrapped with the step's name where it is returned.
var errNeedsRoot = errors.New("root is needed and passwordless sudo is unavailable")

// Seams for tests.
var (
	vkmsLoadPrivileged = loadVkmsPrivileged
	vkmsIsLoaded       = vkmsModuleLoaded
	vkmsIsAvailable    = vkmsModuleAvailable
	vkmsPostLoadCheck  = vkmsKernelLogRecheck
	vkmsOutputPoll     = 250 * time.Millisecond
)

var vkmsSetup struct {
	mu       sync.Mutex
	running  bool
	lastFail time.Time
}

// ensureX11VirtualDisplayAsync makes sure an X11 session has a virtual
// output, in the background. reason is for the log. mayPrompt allows a
// one-time graphical password prompt; it is only set when a tablet connects,
// never at login.
func ensureX11VirtualDisplayAsync(reason string, mayPrompt bool) {
	if !x11SessionActive() {
		return
	}
	go ensureX11VirtualDisplay(reason, mayPrompt)
}

func ensureX11VirtualDisplay(reason string, mayPrompt bool) {
	if !x11SessionActive() || strings.TrimSpace(os.Getenv(x11OutputEnv)) != "" {
		return
	}
	vkmsSetup.mu.Lock()
	if vkmsSetup.running || (!vkmsSetup.lastFail.IsZero() && time.Since(vkmsSetup.lastFail) < vkmsRetryCooldown) {
		vkmsSetup.mu.Unlock()
		return
	}
	vkmsSetup.running = true
	vkmsSetup.mu.Unlock()
	failed := false
	defer func() {
		vkmsSetup.mu.Lock()
		vkmsSetup.running = false
		if failed {
			vkmsSetup.lastFail = time.Now()
		}
		vkmsSetup.mu.Unlock()
	}()

	if x11VirtualDisplayReady() {
		return
	}
	defer refreshX11VkmsWarning()
	if vkmsAutoloadEnabled() && !vkmsBlockedFn() && (!vkmsIsLoaded() || vkmsIsHeld()) && vkmsIsAvailable() {
		// Primary method; guarded: see vkmsAutoloadEnv.
		ready, tryFallback := loadVkmsForX11(reason, mayPrompt, &failed)
		if ready || !tryFallback {
			return
		}
	}
	// Fallback for systems where vkms is unavailable, not picked up by the X
	// server, or unsafe: use a spare, unplugged connector of the GPU.
	if x11VirtualDisplayReady() {
		return
	}
	if why, denied := displayGuardDenied(guardMethodForcedConnector); denied {
		log.Printf("Not using a spare connector as a virtual display: %s; the tablet will mirror the desktop", why)
		return
	}
	displayGuardBegin(guardMethodForcedConnector)
	err := ensureForcedConnectorDisplay(reason, mayPrompt)
	displayGuardEnd(guardMethodForcedConnector)
	if err != nil {
		failed = mayPrompt || !errors.Is(err, errNeedsRoot)
		if !errors.Is(err, errNoSpareConnector) {
			log.Printf("Could not set up a spare-connector virtual display: %v; the tablet will mirror the desktop", err)
		}
		return
	}
	settleNewX11Output()
}

// loadVkmsForX11 loads vkms, checks it while the X server cannot see it, and
// only then lets X use it. ready reports that the virtual display is up.
// Otherwise *failed says whether the attempt counts towards the retry
// cooldown, and tryFallback whether the spare-connector fallback may run in
// the same pass.
//
// The order is what keeps an X11 session safe on kernels where vkms cannot
// show the GPU's buffers (see vkms_probe_linux.go): vkms is loaded held, so X
// never adds it; this process repeats X's mode set on it; a refusal unloads
// it again and denies vkms for this kernel series, while X never knew the
// device, so the fallback runs in a clean session. After release, the kernel
// log and the X log are checked as before; if X still failed on vkms, vkms is
// taken away from X live (vkms-unload) instead of leaving a broken output,
// and no fallback runs in that pass, so the X server is changed only once.
func loadVkmsForX11(reason string, mayPrompt bool, failed *bool) (ready, tryFallback bool) {
	if panicOnWarn() {
		*failed = true
		log.Printf("X11 virtual display: kernel.panic_on_warn is set; not loading vkms, since a refused vkms buffer would crash the computer")
		return false, true
	}
	log.Printf("X11 has no virtual display (%s); loading the vkms kernel module held from the X server, to check it first", reason)
	displayGuardBegin(guardMethodVkms)
	defer displayGuardEnd(guardMethodVkms)
	if err := vkmsLoadPrivileged(mayPrompt); err != nil {
		// A no-prompt attempt that lacked root must not start the cooldown,
		// or it would block the prompt when a tablet connects soon after.
		*failed = mayPrompt || !errors.Is(err, errNeedsRoot)
		log.Printf("Could not load vkms automatically: %v; the tablet will mirror the desktop", err)
		return false, false
	}
	if err := vkmsVerifyFn(mayPrompt); err != nil {
		*failed = true
		var scanout *vkmsScanoutError
		if errors.As(err, &scanout) {
			displayGuardDenyUntil(guardMethodVkms, "the kernel cannot show the GPU's frames on vkms ("+scanout.Err.Error()+")", true)
			log.Printf("X11 virtual display: %v; vkms is not used on this kernel. The X server never saw it; unloading it and trying a spare connector instead", err)
		} else {
			log.Printf("X11 virtual display: vkms could not be checked (%v); not showing it to the X server, unloading it and trying a spare connector instead", err)
		}
		// Unloading may need a second prompt; if it is declined, the held
		// device stays invisible to X and is gone after the next boot.
		if uerr := vkmsUnloadHeldFn(mayPrompt); uerr != nil {
			log.Printf("X11 virtual display: vkms stays loaded but hidden from the X server until the next boot: %v", uerr)
		}
		return false, true
	}
	xmark := markXorgLogs()
	failedInX := func() bool { return vkmsPostLoadCheck() || vkmsXLogFailureFn(xmark) }
	recoverFromX := func() (bool, bool) {
		*failed = true
		displayGuardDenyUntil(guardMethodVkms, "the X server failed to use vkms after it passed the check", true)
		log.Printf("X11 virtual display: the X server failed to use vkms; taking it away from X and unloading it")
		if err := vkmsUnloadHeldFn(true); err != nil {
			log.Printf("X11 virtual display: could not take vkms away from the X server: %v; use Unload vkms in the settings page", err)
		}
		refreshX11VkmsWarning()
		return false, false
	}
	deadline := time.Now().Add(vkmsOutputWait)
	for !x11VirtualDisplayReady() {
		if failedInX() {
			return recoverFromX()
		}
		if time.Now().After(deadline) {
			*failed = true
			log.Printf("vkms is loaded but the X server shows no virtual output; log out and back in to use it")
			return false, false
		}
		time.Sleep(vkmsOutputPoll)
	}
	// The desktop may switch the new output on by itself within a moment.
	for end := time.Now().Add(vkmsSettleWatch); ; {
		if failedInX() {
			return recoverFromX()
		}
		if time.Now().After(end) {
			break
		}
		time.Sleep(vkmsOutputPoll)
	}
	log.Printf("vkms checked and loaded; the X11 virtual display is ready")
	settleNewX11Output()
	return true, false
}

// settleNewX11Output switches the new output off again if the window manager
// turned it on by itself and no session uses it: the host switches it on
// only while a tablet is connected.
func settleNewX11Output() {
	time.Sleep(2 * time.Second)
	display := os.Getenv("DISPLAY")
	x11Ext.mu.Lock()
	defer x11Ext.mu.Unlock()
	if x11Ext.claimed || x11Ext.exiting {
		return
	}
	st, err := queryXrandr(display)
	if err != nil {
		return
	}
	o, err := pickX11VirtualOutput(st, "")
	if err != nil || !o.Active {
		return
	}
	if _, _, err := switchOffX11Output(display, o.Name); err != nil {
		log.Printf("Could not switch the new virtual output %s off: %v", o.Name, err)
	}
}

// loadVkmsPrivileged loads vkms held from the X server (display helper
// action vkms-load, which installs 72-drawsplay-vkms-hold.rules first and
// hands the held device to this user for the check): as root, through
// passwordless sudo, or, when mayPrompt is set, through one polkit prompt.
// Nothing is made to load at boot. Without mayPrompt it never prompts.
func loadVkmsPrivileged(mayPrompt bool) error {
	_, err := runDisplayHelperFn(mayPrompt, "vkms-load")
	if errors.Is(err, errPasswordPromptDismissed) {
		return errVkmsPromptDeclined
	}
	return err
}

// errVkmsInUse means the boot config is gone but the X server would not let
// go of vkms, so it unloads only after logging out or rebooting.
var errVkmsInUse = errors.New("vkms will not load at the next boot, but the X server is still using it; log out and back in (or reboot) to finish unloading it")

// unloadVkmsPrivileged takes vkms away from the X server if X uses it,
// unloads it and removes any boot config (display helper action
// vkms-unload).
func unloadVkmsPrivileged(mayPrompt bool) error {
	out, err := runDisplayHelperFn(mayPrompt, "vkms-unload")
	if err == nil {
		log.Printf("X11 virtual display: vkms unloaded")
		return nil
	}
	if strings.Contains(strings.ToLower(out), "in use") {
		return errVkmsInUse
	}
	return err
}

// Seams for the check that runs between loading vkms and showing it to X.
var (
	vkmsVerifyFn       = verifyAndReleaseVkms
	vkmsUnloadHeldFn   = unloadVkmsPrivileged
	probeVkmsScanoutFn = probeVkmsScanout
	vkmsHeldFn         = vkmsHeldFromX
	vkmsXLogFailureFn  = xorgLogShowsVkmsFailureSince
	// vkmsIsHeld: vkms is loaded but still hidden from the X server (an
	// earlier check did not finish); it is checked again like a fresh load.
	vkmsIsHeld = func() bool {
		node, ok := vkmsCardNode(sysfsDRMDir)
		return ok && vkmsHeldFn(node)
	}
)

// errVkmsNotHeld: vkms is loaded but not held, so the X server may already
// use it and the check cannot run (it needs to be vkms's only user).
var errVkmsNotHeld = errors.New("vkms is loaded but not held from the X server, so it cannot be checked")

// verifyAndReleaseVkms checks the held vkms device against every GPU and
// releases it to the X server only when all of them passed.
func verifyAndReleaseVkms(mayPrompt bool) error {
	node, ok := vkmsCardNode(sysfsDRMDir)
	if !ok {
		return errors.New("vkms is loaded but has no display device")
	}
	if !vkmsHeldFn(node) {
		return errVkmsNotHeld
	}
	gpus := realGPUCards(sysfsDRMDir)
	if err := probeVkmsScanoutFn(node, gpus); err != nil {
		return err
	}
	log.Printf("X11 virtual display: vkms shows frames from %s; releasing it to the X server", strings.Join(gpus, ", "))
	if _, err := runDisplayHelperFn(mayPrompt, "vkms-release"); err != nil {
		return fmt.Errorf("releasing vkms to the X server: %w", err)
	}
	return nil
}

// vkmsHeldFromX reads the udev database: a held vkms card carries the seat
// name 72-drawsplay-vkms-hold.rules gives it.
func vkmsHeldFromX(node string) bool {
	if devnum, err := os.ReadFile(filepath.Join(sysfsDRMDir, filepath.Base(node), "dev")); err == nil {
		if db, err := os.ReadFile(filepath.Join(udevDataDir, "c"+strings.TrimSpace(string(devnum)))); err == nil {
			return udevPropertiesHold(string(db), "E:")
		}
	}
	// No readable database: ask udev.
	out, err := exec.Command("udevadm", "info", "-q", "property", "-n", node).Output()
	return err == nil && udevPropertiesHold(string(out), "")
}

func udevPropertiesHold(props, prefix string) bool {
	for _, line := range strings.Split(props, "\n") {
		if strings.TrimSpace(line) == prefix+"ID_SEAT="+vkmsHoldSeat {
			return true
		}
	}
	return false
}

// udevDataDir is udev's device database (a variable for tests).
var udevDataDir = "/run/udev/data"

// vkmsHoldSeat is the seat name the hold rule gives vkms; X and the
// compositors only use seat0 (or their own seat), and logind rejects it.
const vkmsHoldSeat = "drawsplay-hold"

// xorgLogPaths are where the X server of this session logs: rootless Xorg
// writes to the user's data directory, a root one to /var/log.
func xorgLogPaths() []string {
	display := strings.TrimPrefix(strings.SplitN(os.Getenv("DISPLAY"), ".", 2)[0], ":")
	if display == "" {
		display = "0"
	}
	name := "Xorg." + display + ".log"
	paths := []string{filepath.Join("/var/log", name)}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".local", "share", "xorg", name))
	}
	return paths
}

// xorgLogMark remembers how long each X log was, so a later check reads only
// what X wrote after vkms was released.
type xorgLogMark map[string]int64

func markXorgLogs() xorgLogMark {
	m := xorgLogMark{}
	for _, p := range xorgLogPaths() {
		if fi, err := os.Stat(p); err == nil {
			m[p] = fi.Size()
		}
	}
	return m
}

// xorgLogShowsVkmsFailureSince reports whether X logged a failed mode set on
// a GPU screen (where it puts vkms) after mark. The kernel's WARN is printed
// once per boot, so the X log is the signal for every later failure.
func xorgLogShowsVkmsFailureSince(mark xorgLogMark) bool {
	for _, p := range xorgLogPaths() {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		data, err := func() ([]byte, error) {
			defer f.Close()
			if _, err := f.Seek(mark[p], 0); err != nil {
				return nil, err
			}
			return io.ReadAll(io.LimitReader(f, 4<<20))
		}()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "(EE) modeset(G") && strings.Contains(line, "failed to set mode") {
				return true
			}
		}
	}
	return false
}

// setUpX11VirtualDisplayNow is the Web UI's "Set up virtual display" button:
// the same guarded setup as when a tablet connects (vkms, else a spare HDMI
// connector), allowed to prompt for a password and not held back by an
// earlier failure's cooldown, since the user asked for it.
func setUpX11VirtualDisplayNow() error {
	vkmsSetup.mu.Lock()
	if vkmsSetup.running {
		vkmsSetup.mu.Unlock()
		return errors.New("the virtual display is already being set up; try again in a moment")
	}
	vkmsSetup.lastFail = time.Time{}
	vkmsSetup.mu.Unlock()
	ensureX11VirtualDisplay("Web UI setup", true)
	if x11VirtualDisplayReady() {
		return nil
	}
	if w := x11VirtualDisplayWarning(); w != "" {
		return errors.New(w)
	}
	return errors.New("no virtual display could be set up; the tablet mirrors the desktop (see the host log)")
}
