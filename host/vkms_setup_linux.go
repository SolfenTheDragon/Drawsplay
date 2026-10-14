//go:build linux

package main

// Automatic setup of the X11 virtual display: loads the vkms kernel module
// when an X11 session has no virtual RandR output. It runs when the host
// starts and whenever a tablet is newly connected. Loading a module needs
// root, so when sudo wants a password the user gets a graphical prompt
// (polkit, or a zenity/kdialog password dialog).

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
	// vkmsPromptCooldown keeps a declined or failed attempt from prompting
	// again on every reconnect.
	vkmsPromptCooldown = 2 * time.Minute
	vkmsOutputWait     = 5 * time.Second
	vkmsPasswordTries  = 3

	// Newer kernels create no virtual device unless create_default_dev=1;
	// older kernels reject that parameter, so fall back to a plain load.
	vkmsLoadScript = "modprobe vkms create_default_dev=1 || modprobe vkms"
)

var errVkmsPromptDeclined = errors.New("the password prompt was dismissed")

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
// output, in the background. reason is for the log.
func ensureX11VirtualDisplayAsync(reason string) {
	if !x11SessionActive() {
		return
	}
	go ensureX11VirtualDisplay(reason)
}

func ensureX11VirtualDisplay(reason string) {
	if !x11SessionActive() || strings.TrimSpace(os.Getenv(x11OutputEnv)) != "" {
		return
	}
	vkmsSetup.mu.Lock()
	if vkmsSetup.running || (!vkmsSetup.lastFail.IsZero() && time.Since(vkmsSetup.lastFail) < vkmsPromptCooldown) {
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
	if err := vkmsLoadPrivileged(); err != nil {
		failed = true
		log.Printf("Could not load vkms: %v; the tablet will mirror the desktop", err)
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

// loadVkmsPrivileged loads vkms as root: directly, through passwordless
// sudo, through a polkit prompt, or through sudo with a password dialog.
func loadVkmsPrivileged() error {
	script := vkmsLoadScript
	env := append(os.Environ(), "PATH="+os.Getenv("PATH")+":/usr/sbin:/sbin")
	run := func(stdin string, name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Env = env
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(out.String()))
		}
		return nil
	}
	if os.Geteuid() == 0 {
		return run("", "sh", "-c", script)
	}
	sudo, sudoErr := exec.LookPath("sudo")
	if sudoErr == nil {
		if err := run("", sudo, "-n", "sh", "-c", script); err == nil {
			return nil
		}
	}
	if pkexec, err := exec.LookPath("pkexec"); err == nil {
		err = run("", pkexec, "sh", "-c", script)
		if err == nil {
			return nil
		}
		var ee *exec.ExitError
		// 126: the user dismissed the prompt or is not authorized.
		if errors.As(err, &ee) && ee.ExitCode() == 126 {
			return errVkmsPromptDeclined
		}
		log.Printf("pkexec could not load vkms (%v); trying a password dialog", err)
	}
	if sudoErr != nil {
		return errors.New("neither sudo nor pkexec is available")
	}
	for attempt := 0; attempt < vkmsPasswordTries; attempt++ {
		prompt := "DrawSplay needs your password to load the vkms kernel module so the tablet can extend your desktop."
		if attempt > 0 {
			prompt = "Wrong password, try again. " + prompt
		}
		password, err := askPasswordDialog(prompt)
		if err != nil {
			return err
		}
		if err := run(password+"\n", sudo, "-S", "-p", "", "sh", "-c", script); err == nil {
			return nil
		} else if attempt == vkmsPasswordTries-1 {
			return err
		}
	}
	return errVkmsPromptDeclined
}

// askPasswordDialog shows a graphical password prompt and returns what the
// user typed.
func askPasswordDialog(text string) (string, error) {
	var cmd *exec.Cmd
	if zenity, err := exec.LookPath("zenity"); err == nil {
		cmd = exec.Command(zenity, "--password", "--title=DrawSplay", "--text="+text)
	} else if kdialog, err := exec.LookPath("kdialog"); err == nil {
		cmd = exec.Command(kdialog, "--title", "DrawSplay", "--password", text)
	} else {
		return "", errors.New("no graphical password prompt available (install zenity or kdialog)")
	}
	out, err := cmd.Output()
	if err != nil {
		return "", errVkmsPromptDeclined
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}
