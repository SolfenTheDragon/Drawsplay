//go:build linux

package main

// Automatic setup of the X11 virtual display: loads the vkms kernel module
// when an X11 session has no virtual RandR output. It runs when the host
// starts and whenever a tablet is newly connected. Loading a module needs
// root: the host uses root or passwordless sudo, and when a tablet connects it
// may show one polkit password prompt, which also configures vkms to load at
// boot. It never prompts at login.

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

var errVkmsPromptDeclined = errors.New("the password prompt was dismissed")

var errVkmsNeedsRoot = errors.New("loading vkms needs root and passwordless sudo is unavailable")

// Seams for tests.
var (
	vkmsLoadPrivileged = loadVkmsPrivileged
	vkmsIsLoaded       = vkmsModuleLoaded
	vkmsIsAvailable    = vkmsModuleAvailable
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
	if vkmsIsLoaded() {
		// The module is there but the X server has not picked it up; only a
		// new X session fixes that.
		return
	}
	if !vkmsIsAvailable() {
		return
	}
	log.Printf("X11 has no virtual display (%s); loading the vkms kernel module", reason)
	if err := vkmsLoadPrivileged(mayPrompt); err != nil {
		// A no-prompt attempt that lacked root must not start the cooldown,
		// or it would block the prompt when a tablet connects soon after.
		failed = mayPrompt || !errors.Is(err, errVkmsNeedsRoot)
		log.Printf("Could not load vkms automatically: %v; the tablet will mirror the desktop", err)
		return
	}
	deadline := time.Now().Add(vkmsOutputWait)
	for !x11VirtualDisplayReady() {
		if time.Now().After(deadline) {
			failed = true
			log.Printf("vkms is loaded but the X server shows no virtual output; log out and back in to use it")
			return
		}
		time.Sleep(vkmsOutputPoll)
	}
	log.Printf("vkms loaded; the X11 virtual display is ready")
	settleNewX11Output()
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

// vkmsLoadAndPersistScript loads vkms and, on success, configures it to load
// at boot so the prompt is needed only once.
const vkmsLoadAndPersistScript = `(modprobe vkms create_default_dev=1 || modprobe vkms) && {
[ -e /etc/modprobe.d/drawsplay-vkms.conf ] || echo 'options vkms create_default_dev=1' > /etc/modprobe.d/drawsplay-vkms.conf
[ -e /etc/modules-load.d/drawsplay-vkms.conf ] || echo vkms > /etc/modules-load.d/drawsplay-vkms.conf
true
}`

// loadVkmsPrivileged loads vkms as root: directly, through passwordless sudo,
// or, when mayPrompt is set, through one polkit (pkexec) password prompt that
// also makes vkms load at boot. Without mayPrompt it never prompts.
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
		return run("sh", "-c", vkmsLoadAndPersistScript)
	}
	if sudo, err := exec.LookPath("sudo"); err == nil {
		for _, argv := range vkmsLoadCommands {
			if run(append([]string{sudo, "-n"}, argv...)...) == nil {
				return nil
			}
		}
	}
	if !mayPrompt {
		return errVkmsNeedsRoot
	}
	pkexec, err := exec.LookPath("pkexec")
	if err != nil {
		return errors.New("pkexec is not available; run \"sudo modprobe vkms create_default_dev=1\" and configure vkms to load at boot")
	}
	err = run(pkexec, "sh", "-c", vkmsLoadAndPersistScript)
	var ee *exec.ExitError
	// 126: the user dismissed the prompt or is not authorized.
	if errors.As(err, &ee) && ee.ExitCode() == 126 {
		return errVkmsPromptDeclined
	}
	return err
}
