//go:build linux

package main

// Automatic setup of the X11 virtual display: loads the vkms kernel module
// when an X11 session has no virtual RandR output. It runs when the host
// starts and whenever a tablet is newly connected. Loading a module needs
// root: the host uses root or passwordless sudo, and when a tablet connects it
// may show one polkit password prompt. It never prompts at login. See vkmsAutoloadEnv
// for when it is skipped.

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
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
	if vkmsAutoloadEnabled() && !vkmsBlockedFn() && !vkmsIsLoaded() && vkmsIsAvailable() {
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

// loadVkmsForX11 loads vkms and waits for its output. ready reports that the
// virtual display is up. Otherwise *failed says whether the attempt counts
// towards the retry cooldown, and tryFallback whether the spare-connector
// fallback should be tried in the same pass: it is when the kernel rejected
// vkms, not when the user declined the prompt (the fallback would ask again)
// or the output simply has not appeared (a re-login fixes that). The kernel
// log is read again right after the load and once the output is up, because
// the vkms_prepare_fb failure strikes within that second and can leave the X
// server alive with a frozen display: a failure found there denies vkms for
// this kernel series so no later start repeats it.
func loadVkmsForX11(reason string, mayPrompt bool, failed *bool) (ready, tryFallback bool) {
	log.Printf("X11 has no virtual display (%s); loading the vkms kernel module", reason)
	displayGuardBegin(guardMethodVkms)
	defer displayGuardEnd(guardMethodVkms)
	if err := vkmsLoadPrivileged(mayPrompt); err != nil {
		// A no-prompt attempt that lacked root must not start the cooldown,
		// or it would block the prompt when a tablet connects soon after.
		*failed = mayPrompt || !errors.Is(err, errNeedsRoot)
		log.Printf("Could not load vkms automatically: %v; the tablet will mirror the desktop", err)
		return false, false
	}
	kernelFailed := func() (bool, bool) {
		*failed = true
		log.Printf("vkms failed in the kernel right after loading; leaving it alone and trying a spare connector instead")
		return false, true
	}
	deadline := time.Now().Add(vkmsOutputWait)
	for !x11VirtualDisplayReady() {
		if vkmsPostLoadCheck() {
			return kernelFailed()
		}
		if time.Now().After(deadline) {
			*failed = true
			log.Printf("vkms is loaded but the X server shows no virtual output; log out and back in to use it")
			return false, false
		}
		time.Sleep(vkmsOutputPoll)
	}
	if vkmsPostLoadCheck() {
		return kernelFailed()
	}
	log.Printf("vkms loaded; the X11 virtual display is ready")
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

// vkmsLoadCommands are tried in order. Newer kernels create no virtual device
// unless create_default_dev=1; older kernels reject that parameter, so fall
// back to a plain load.
var vkmsLoadCommands = [][]string{
	{"modprobe", "vkms", "create_default_dev=1"},
	{"modprobe", "vkms"},
}

// vkmsLoadScript loads vkms (opt-in only, see vkmsAutoloadEnv).
// It deliberately does not persist anything: loading vkms at boot would
// expose every login to the kernel issue described at vkmsAutoloadEnv.
const vkmsLoadScript = `modprobe vkms create_default_dev=1 || modprobe vkms`

// loadVkmsPrivileged loads vkms as root: directly, through passwordless sudo,
// or, when mayPrompt is set, through one polkit (pkexec) password prompt.
// Nothing is made to load at boot. Without mayPrompt it never prompts.
func loadVkmsPrivileged(mayPrompt bool) error {
	env := append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/sbin:/sbin")
	run := func(argv ...string) error {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w (%s)", strings.Join(argv, " "), err, strings.TrimSpace(out.String()))
		}
		return nil
	}
	if os.Geteuid() == 0 {
		return run("sh", "-c", vkmsLoadScript)
	}
	if sudo, err := exec.LookPath("sudo"); err == nil {
		for _, argv := range vkmsLoadCommands {
			if run(append([]string{sudo, "-n"}, argv...)...) == nil {
				return nil
			}
		}
	}
	if !mayPrompt {
		return fmt.Errorf("loading vkms: %w", errNeedsRoot)
	}
	pkexec, err := exec.LookPath("pkexec")
	if err != nil {
		return errors.New("pkexec is not available; run \"sudo modprobe vkms create_default_dev=1\" (vkms is not loaded at boot; it is loaded when needed)")
	}
	if helper, ok := displayHelper(); ok {
		err = run(pkexec, helper, "vkms-load")
	} else {
		err = run(pkexec, "sh", "-c", vkmsLoadScript)
	}
	var ee *exec.ExitError
	// 126: the user dismissed the prompt or is not authorized.
	if errors.As(err, &ee) && ee.ExitCode() == 126 {
		return errVkmsPromptDeclined
	}
	return err
}
