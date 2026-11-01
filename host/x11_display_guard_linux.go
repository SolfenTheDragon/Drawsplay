//go:build linux

package main

// Crash recovery for the X11 virtual display methods.
//
// Two methods can take the display down on some kernels: hot-adding or using
// a vkms output (Linux 7.0 OEM: a kernel WARNING in vkms_prepare_fb /
// dma_buf_vmap when X first sets a mode on it) and forcing a spare connector.
// The host cannot catch a kernel or X server failure, so it keeps a small
// state file next to its config:
//
//   - Before a risky step it records the method as "pending" and clears the
//     mark once the step has finished. A mark still present when the host
//     starts means the host or the display server died in the middle of that
//     step. The method is then denied, the host undoes what it can (a forced
//     connector goes back to autodetect) and the next method is tried; with
//     none left the tablet mirrors the desktop.
//   - A method that was denied stays off until the denial expires, so a
//     restarted session does not walk into the same failure again.
//   - Separately, vkms is treated as unusable when this boot's kernel log
//     already shows the vkms_prepare_fb failure, whoever loaded the module
//     (the install script, modules-load.d, the user).
//
// DRAWSPLAY_X11_VKMS_AUTOLOAD=1 overrides every vkms check: it is the
// documented way for a user who has verified vkms on their kernel. A denial
// ends by itself when the kernel release changes.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	guardMethodVkms            = "vkms"
	guardMethodForcedConnector = "forced-connector"

	// displayGuardDenialTTL is how long a failed method stays off. A new
	// kernel or X server may fix it, so it is not permanent.
	displayGuardDenialTTL = 7 * 24 * time.Hour

	displayGuardFile = "display-guard.json"
)

type displayGuardDenial struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
	// Kernel is the running kernel release when the denial was recorded; a
	// different kernel (an update may fix the bug) ends the denial.
	Kernel string `json:"kernel,omitempty"`
	// UntilKernelChange keeps the denial for as long as the kernel stays the
	// same, however long that is: used when the kernel log itself proved the
	// failure, so a new boot does not repeat it.
	UntilKernelChange bool `json:"untilKernelChange,omitempty"`
}

type displayGuardState struct {
	Pending map[string]time.Time          `json:"pending,omitempty"`
	Denied  map[string]displayGuardDenial `json:"denied,omitempty"`
}

// Seams for tests.
var (
	displayGuardDir  = configDir
	displayGuardNow  = time.Now
	kernelReleaseFn  = kernelRelease
	vkmsBlockedFn    = vkmsBlocked
	vkmsKernelLogFn  = vkmsKernelLogShowsFailure
	undoForcedOutput = undoForcedConnectors
)

var displayGuardMu sync.Mutex

var vkmsOutputPattern = regexp.MustCompile(`^Virtual-\d+-\d+$`)

// guardMethodForOutput names the method behind a RandR output, or "" for one
// the guard does not cover (a real or Intel VIRTUAL head, a user-chosen one).
func guardMethodForOutput(name string) string {
	switch {
	case vkmsOutputPattern.MatchString(name):
		return guardMethodVkms
	case isForcedX11Output(name):
		return guardMethodForcedConnector
	}
	return ""
}

func displayGuardPath() (string, error) {
	dir, err := displayGuardDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, displayGuardFile), nil
}

// loadDisplayGuardLocked reads the state; a missing or corrupt file is an
// empty state. The caller holds displayGuardMu.
func loadDisplayGuardLocked() displayGuardState {
	var st displayGuardState
	path, err := displayGuardPath()
	if err != nil {
		return st
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func saveDisplayGuardLocked(st displayGuardState) {
	path, err := displayGuardPath()
	if err != nil {
		return
	}
	if len(st.Pending) == 0 && len(st.Denied) == 0 {
		_ = os.Remove(path)
		return
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	// Written and synced before the risky step starts: a crash right after
	// must still leave the mark behind.
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// displayGuardBegin records that a risky step for the method is starting.
func displayGuardBegin(method string) {
	if method == "" {
		return
	}
	displayGuardMu.Lock()
	defer displayGuardMu.Unlock()
	st := loadDisplayGuardLocked()
	if st.Pending == nil {
		st.Pending = map[string]time.Time{}
	}
	st.Pending[method] = displayGuardNow()
	saveDisplayGuardLocked(st)
}

// displayGuardEnd records that the step finished, whatever its outcome, with
// the display server still alive.
func displayGuardEnd(method string) {
	if method == "" {
		return
	}
	displayGuardMu.Lock()
	defer displayGuardMu.Unlock()
	st := loadDisplayGuardLocked()
	if _, ok := st.Pending[method]; !ok {
		return
	}
	delete(st.Pending, method)
	saveDisplayGuardLocked(st)
}

func kernelRelease() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func displayGuardDeny(method, reason string) { displayGuardDenyUntil(method, reason, false) }

func displayGuardDenyUntil(method, reason string, untilKernelChange bool) {
	displayGuardMu.Lock()
	defer displayGuardMu.Unlock()
	st := loadDisplayGuardLocked()
	if st.Denied == nil {
		st.Denied = map[string]displayGuardDenial{}
	}
	st.Denied[method] = displayGuardDenial{
		Reason: reason, At: displayGuardNow(),
		Kernel: kernelReleaseFn(), UntilKernelChange: untilKernelChange,
	}
	delete(st.Pending, method)
	saveDisplayGuardLocked(st)
}

// displayGuardDenied reports whether the method is denied, and why.
func displayGuardDenied(method string) (string, bool) {
	displayGuardMu.Lock()
	defer displayGuardMu.Unlock()
	d, ok := loadDisplayGuardLocked().Denied[method]
	if !ok {
		return "", false
	}
	if d.Kernel != "" && d.Kernel != kernelReleaseFn() {
		return "", false
	}
	if !d.UntilKernelChange && displayGuardNow().Sub(d.At) > displayGuardDenialTTL {
		return "", false
	}
	return d.Reason, true
}

// recoverDisplayGuard runs when the host starts, before any virtual display
// is set up. It turns pending marks left by a previous process into denials
// and undoes what can be undone without risk.
func recoverDisplayGuard() {
	displayGuardMu.Lock()
	st := loadDisplayGuardLocked()
	var failed []string
	for method := range st.Pending {
		failed = append(failed, method)
	}
	if len(failed) > 0 {
		if st.Denied == nil {
			st.Denied = map[string]displayGuardDenial{}
		}
		for _, m := range failed {
			st.Denied[m] = displayGuardDenial{
				Reason: "the host or display server stopped while it was being set up",
				At:     displayGuardNow(),
			}
		}
		st.Pending = nil
		saveDisplayGuardLocked(st)
	}
	displayGuardMu.Unlock()

	for _, m := range failed {
		log.Printf("X11 virtual display: %s did not finish setting up last time (the host or display server stopped); not using it again for %d days", m, int(displayGuardDenialTTL/(24*time.Hour)))
		switch m {
		case guardMethodForcedConnector:
			undoForcedOutput()
		case guardMethodVkms:
			log.Printf("X11 virtual display: vkms stays loaded until you unload it (sudo modprobe -r vkms) or remove its boot config (/etc/modules-load.d/drawsplay-vkms.conf)")
		}
	}
}

// undoForcedConnectors returns every forced spare connector to autodetect so
// a half-configured one cannot linger as a phantom monitor.
func undoForcedConnectors() {
	for _, c := range listHDMIConnectors() {
		if c.Status == "connected" && c.NoEDID {
			if err := forceConnectorPrivileged(filepath.Join(c.Dir, "status"), "detect", false); err != nil {
				log.Printf("X11 virtual display: could not release the forced connector %s: %v", c.XName, err)
			} else {
				log.Printf("X11 virtual display: released the forced connector %s", c.XName)
			}
		}
	}
}

// vkmsBlocked reports whether vkms must not be used as a virtual display:
// a denial from an earlier failure, or a vkms failure in this boot's kernel
// log. An explicit opt-in (DRAWSPLAY_X11_VKMS_AUTOLOAD=1) overrides both.
func vkmsBlocked() bool {
	if vkmsForcedByUser() {
		return false
	}
	if _, denied := displayGuardDenied(guardMethodVkms); denied {
		return true
	}
	return vkmsKernelLogFn()
}

// The kernel log is read at most once a minute once it came back clean; a
// failure sticks for the rest of the boot.
var vkmsLogCache struct {
	mu      sync.Mutex
	checked time.Time
	failed  bool
}

func vkmsKernelLogShowsFailure() bool {
	vkmsLogCache.mu.Lock()
	defer vkmsLogCache.mu.Unlock()
	if vkmsLogCache.failed {
		return true
	}
	if !vkmsLogCache.checked.IsZero() && time.Since(vkmsLogCache.checked) < time.Minute {
		return false
	}
	vkmsLogCache.checked = time.Now()
	if !vkmsModuleLoaded() {
		return false
	}
	text, ok := readKernelLog()
	if ok && kernelLogShowsVkmsFailure(text) {
		vkmsLogCache.failed = true
		// Remembered across reboots for this kernel: the next boot's log is
		// clean until vkms is tried again, and trying again crashes X.
		displayGuardDenyUntil(guardMethodVkms, "the kernel log shows a vkms failure (vkms_prepare_fb)", true)
		log.Printf("X11 virtual display: the kernel log shows a vkms failure (vkms_prepare_fb); not using vkms outputs. Set %s=1 to override", vkmsAutoloadEnv)
		return true
	}
	return false
}

// kernelLogShowsVkmsFailure matches the WARNING/oops trace of the known
// vkms_prepare_fb → dma_buf_vmap failure.
func kernelLogShowsVkmsFailure(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "vkms_prepare_fb") {
			return true
		}
	}
	return false
}

func readKernelLog() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, argv := range [][]string{
		{"journalctl", "-k", "-b", "--no-pager", "-q"},
		{"dmesg"},
	} {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, path, argv[1:]...).Output()
		if err == nil {
			return string(out), true
		}
	}
	return "", false
}
