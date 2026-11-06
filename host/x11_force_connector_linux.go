//go:build linux

package main

// Fallback virtual display for X11 sessions where the vkms path is not
// available or not safe (for example Linux 7.0 OEM, where hot-adding vkms to
// X's modesetting driver trips a kernel warning and took the session down).
//
// The GPU's own spare connectors (an unplugged HDMI port) can be forced to
// "connected" through the DRM sysfs `status` attribute. The kernel then
// reports the connector with default modes and no EDID, X exposes it as an
// ordinary RandR output, and the host extends the desktop onto it exactly as
// it does for a vkms output: it adds the tablet's mode with xrandr, switches
// the output on and records its rectangle. Nothing is hot-added to the X
// server, no kernel module is loaded, and nothing persists across boots.
//
// Only HDMI-A connectors of PCI GPUs are used: forcing a DisplayPort or
// USB-C connector would start link training towards a sink that is not there.
// A forced connector is recognised later (after a host restart) by being
// connected without an EDID, which a real monitor always has.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var errNoSpareConnector = errors.New("no unplugged HDMI connector available")

// sysfsDRMDir is a seam for tests.
var sysfsDRMDir = "/sys/class/drm"

// forceConnectorPrivileged writes value ("on" or "detect") to the connector's
// status file as root. It is a seam for tests.
var forceConnectorPrivileged = writeConnectorStatusPrivileged

// connectorDirectWrite is a seam for tests.
var connectorDirectWrite = func(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(value + "\n")
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

var drmConnectorDir = regexp.MustCompile(`^card\d+-(HDMI-A-\d+)$`)

type drmHDMIConnector struct {
	Dir    string // /sys/class/drm/card0-HDMI-A-1
	XName  string // RandR output name: HDMI-1
	Status string // sysfs status: connected / disconnected
	NoEDID bool   // no monitor identity behind it
}

// listHDMIConnectors returns the HDMI connectors of PCI GPUs.
func listHDMIConnectors() []drmHDMIConnector {
	var out []drmHDMIConnector
	entries, _ := filepath.Glob(filepath.Join(sysfsDRMDir, "card*-HDMI-A-*"))
	for _, dir := range entries {
		m := drmConnectorDir.FindStringSubmatch(filepath.Base(dir))
		if m == nil {
			continue
		}
		// Only real PCI GPUs: not vkms/vgem (faux platform devices).
		if dev, err := filepath.EvalSymlinks(filepath.Join(dir, "device")); err != nil || !strings.Contains(dev, "/pci") {
			continue
		}
		st, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil {
			continue
		}
		edid, _ := os.ReadFile(filepath.Join(dir, "edid"))
		out = append(out, drmHDMIConnector{
			Dir:    dir,
			XName:  strings.Replace(m[1], "HDMI-A-", "HDMI-", 1),
			Status: strings.TrimSpace(string(st)),
			NoEDID: len(edid) == 0,
		})
	}
	return out
}

// isForcedX11Output reports whether the RandR output is a spare connector
// that is connected without a monitor behind it (forced on, by this or a
// previous host process).
func isForcedX11Output(name string) bool {
	if !strings.HasPrefix(name, "HDMI-") {
		return false
	}
	for _, c := range listHDMIConnectors() {
		if c.XName == name {
			return c.Status == "connected" && c.NoEDID
		}
	}
	return false
}

// isX11VirtualOutputName reports whether the output is one the host may use
// as a virtual display.
func isX11VirtualOutputName(name string) bool {
	return x11AutoOutputPattern.MatchString(name) || isForcedX11Output(name)
}

var connectorStatusPath = regexp.MustCompile(`^/sys/class/drm/card\d+-HDMI-A-\d+/status$`)

// ensureForcedConnectorDisplay forces an unplugged HDMI connector on and
// waits for X to list it.
func ensureForcedConnectorDisplay(reason string, mayPrompt bool) error {
	var pick *drmHDMIConnector
	for _, c := range listHDMIConnectors() {
		if c.Status == "disconnected" && !displayClaimedByOther(c.XName) {
			c := c
			pick = &c
			break
		}
	}
	if pick == nil {
		return errNoSpareConnector
	}
	log.Printf("X11 has no virtual display (%s); using the unplugged connector %s as one", reason, pick.XName)
	if err := forceConnectorPrivileged(filepath.Join(pick.Dir, "status"), "on", mayPrompt); err != nil {
		return err
	}
	// Held (forced) for the life of this host, between sessions too, so
	// dd-host's cleanup leaves it alone.
	claimDisplay(displayClaimConnector, pick.XName)
	deadline := time.Now().Add(vkmsOutputWait)
	for !x11VirtualDisplayReady() {
		if time.Now().After(deadline) {
			// Undo the force: a connected output with nothing behind it
			// would otherwise show up as a phantom monitor.
			_ = forceConnectorPrivileged(filepath.Join(pick.Dir, "status"), "detect", false)
			return fmt.Errorf("%s was forced on but the X server shows no usable output", pick.XName)
		}
		time.Sleep(vkmsOutputPoll)
	}
	log.Printf("X11 virtual display ready on %s", pick.XName)
	return nil
}

func writeConnectorStatusPrivileged(path, value string, mayPrompt bool) error {
	if !connectorStatusPath.MatchString(path) || (value != "on" && value != "detect") {
		return fmt.Errorf("refusing to write %q to %q", value, path)
	}
	// With the udev rule installed (packaging/linux/70-drawsplay-drm-connector.rules)
	// the status file is writable by the plugdev group: no root, no prompt.
	if os.Geteuid() != 0 && connectorDirectWrite(path, value) == nil {
		return nil
	}
	// The path and value are passed as arguments, never interpolated.
	script := `echo "$2" > "$1"`
	run := func(argv ...string) error {
		cmd := exec.Command(argv[0], argv[1:]...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w (%s)", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	args := []string{"sh", "-c", script, "sh", path, value}
	if os.Geteuid() == 0 {
		return run(args...)
	}
	if sudo, err := exec.LookPath("sudo"); err == nil {
		if run(append([]string{sudo, "-n"}, args...)...) == nil {
			return nil
		}
	}
	if !mayPrompt {
		return errVkmsNeedsRoot
	}
	pkexec, err := exec.LookPath("pkexec")
	if err != nil {
		return errors.New("pkexec is not available; run: echo on | sudo tee " + path)
	}
	err = run(append([]string{pkexec}, args...)...)
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 126 {
		return errVkmsPromptDeclined
	}
	return err
}

// releaseForcedConnectors returns every spare connector that is forced on
// (connected without an EDID, which a real monitor always has) to the
// kernel's own detection, so no phantom HDMI monitor outlives the host. It
// never prompts: with no root access it reports the failure and leaves the
// connector for the uninstaller or the next host start to clear. It reports
// how many connectors it released.
func releaseForcedConnectors() int {
	released := 0
	for _, c := range listHDMIConnectors() {
		if c.Status != "connected" || !c.NoEDID {
			continue
		}
		// Forced by dd-host (DrawSplay Desktop), which is still using it.
		if displayClaimedByOther(c.XName) {
			continue
		}
		if err := forceConnectorPrivileged(filepath.Join(c.Dir, "status"), "detect", false); err != nil {
			log.Printf("Could not release the forced connector %s: %v (run: echo detect | sudo tee %s)",
				c.XName, err, filepath.Join(c.Dir, "status"))
			continue
		}
		releaseDisplayClaim(displayClaimConnector, c.XName)
		log.Printf("Released the forced connector %s", c.XName)
		released++
	}
	return released
}
