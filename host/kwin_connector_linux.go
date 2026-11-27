//go:build linux

package main

// KDE Plasma extended display on a spare HDMI port (see kwin_extend_linux.go
// for the virtual output it is preferred to).
//
// KWin's virtual outputs have exactly one mode, the size they were made with,
// so Display Configuration cannot change their resolution. A real connector
// can: the host forces an unplugged HDMI port of the GPU on and has the
// kernel give it the tablet's EDID (edid.go) through drm.edid_firmware. KWin
// then sees an ordinary monitor called DrawSplay, lists the tablet's native
// resolution and the standard modes below it, and remembers the mode, scale,
// rotation and place the user picks, per tablet size. The host streams the
// port through KWin's screencast protocol (stream_output) and follows
// whatever mode KWin shows; the host's own resolution setting plays no part.
//
// Root is needed once per tablet size: the display helper writes the EDID
// to /lib/firmware/edid and installs 71-drawsplay-edid-override.rules, which
// lets the plugdev group set drm.edid_firmware from then on (as
// 70-drawsplay-drm-connector.rules does for the port's status). Only the
// host's own entries are added to drm.edid_firmware, ahead of the user's, and
// only while the port is in use; the host removes them, and switches the
// port back to detection, when the display goes, when it exits and when it
// starts after a crash.
//
// Those releases never ask for a password, so the port is used only when the
// host can release it without one: as a member of the plugdev group (with
// the rules in effect), as root or through passwordless sudo. Otherwise a
// phantom DrawSplay monitor would outlast every session.
//
// drm.edid_firmware and KWin name a port by its connector name alone, which
// two GPUs can share (card0-HDMI-A-1, card1-HDMI-A-1); such a port is never
// used, since the override would reach the other card's monitor too.
//
// Without a spare HDMI port, or when the setup fails or the password prompt
// is dismissed, KDE gets the virtual output instead.

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Test hooks.
var (
	kwinEDIDFirmwareDir   = "/lib/firmware/edid"
	edidFirmwareParamPath = "/sys/module/drm/parameters/edid_firmware"
	kwinConnectorWait     = 6 * time.Second
	kwinConnectorPoll     = 100 * time.Millisecond
	kwinStreamOutput      = startKWinOutputStream
	// kwinRootWithoutPrompt reports whether root steps run without a
	// password prompt (the host is root, or sudo needs no password).
	kwinRootWithoutPrompt = rootWithoutPrompt
	// kwinInPlugdev reports whether this process is in the plugdev group.
	kwinInPlugdev        = inPlugdevGroup
	edidParamDirectWrite = func(path string, value []byte) error {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_, werr := f.Write(value)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		return cerr
	}
)

// kwinConnectorRefused: the spare HDMI port cannot be used by this host
// process (the user dismissed the setup's password prompt, or the host could
// not release the port without one), so it does not ask again and uses the
// virtual output. Guarded by wlExt.mu (Create runs under it).
var kwinConnectorRefused error

var (
	errKWinConnectorDeclined = errors.New("the password prompt for setting up the spare HDMI port was dismissed earlier")
	// errKWinConnectorRefusedEarlier wraps the refusal on later tries,
	// which are not logged again.
	errKWinConnectorRefusedEarlier = errors.New("the spare HDMI port was ruled out earlier")
	errKWinConnectorNoAccess       = errors.New("DrawSplay cannot release the spare HDMI port without a password: add yourself to the plugdev group (sudo groupadd -f plugdev && sudo usermod -aG plugdev $USER) and log in again")
)

// kwinConnectorOutput is the host's stream of a forced HDMI port.
type kwinConnectorOutput struct {
	conn drmHDMIConnector
	name string // KWin's output name, the DRM connector's: HDMI-A-1
	k    *kwinOutput
}

// tabletNativeSize is the size the tablet's EDID describes: its panel, or
// the requested size when it reported none.
func tabletNativeSize(w, h int) (int, int) {
	if id := activeClientIdentity(); id.NativeWidth > 0 && id.NativeHeight > 0 {
		return id.NativeWidth, id.NativeHeight
	}
	return w, h
}

// startKWinConnector shows the tablet on a spare HDMI port, as a monitor with
// a w x h panel. It returns errNoSpareConnector when there is no port to use.
func startKWinConnector(w, h int, desktopFileInstalled bool) (*kwinConnectorOutput, error) {
	if kwinConnectorRefused != nil {
		return nil, fmt.Errorf("%w: %w", errKWinConnectorRefusedEarlier, kwinConnectorRefused)
	}
	if why, denied := displayGuardDenied(guardMethodKWinConnector); denied {
		return nil, fmt.Errorf("not forcing a spare HDMI port on: %s", why)
	}
	// Releasing the port never prompts: without a way to do it, using the
	// port would leave a phantom monitor behind.
	if !kwinInPlugdev() && !kwinRootWithoutPrompt() {
		kwinConnectorRefused = errKWinConnectorNoAccess
		return nil, kwinConnectorRefused
	}
	var pick *drmHDMIConnector
	for _, c := range listHDMIConnectors() {
		if c.Status == "disconnected" && !displayClaimedByOther(c.XName) && connectorNameUnique(c) {
			c := c
			pick = &c
			break
		}
	}
	if pick == nil {
		return nil, errNoSpareConnector
	}
	if _, err := os.Stat(edidFirmwareParamPath); err != nil {
		return nil, errors.New("this kernel cannot load an EDID from a file (no drm.edid_firmware)")
	}
	edid, err := tabletEDID(w, h)
	if err != nil {
		return nil, err
	}
	file := tabletEDIDFirmwareName(w, h)
	if err := ensureTabletEDIDInstalled(file, edid); err != nil {
		return nil, err
	}
	name := pick.Name()
	statusPath := filepath.Join(pick.Dir, "status")
	// The rules may not be in effect yet (a group joined since login).
	if !(edidParamWritable() && fileWritable(statusPath)) && !kwinRootWithoutPrompt() {
		kwinConnectorRefused = errKWinConnectorNoAccess
		return nil, kwinConnectorRefused
	}
	log.Printf("KWin extended display: showing the tablet (%dx%d) on the unplugged port %s", w, h, name)

	displayGuardBegin(guardMethodKWinConnector)
	defer displayGuardEnd(guardMethodKWinConnector)
	undo := func() {
		if err := releaseKWinPort(name, statusPath); err != nil {
			log.Printf("KWin extended display: %v", err)
		}
		releaseDisplayClaim(displayClaimConnector, pick.XName)
	}
	if err := setEDIDOverride(name, file, true); err != nil {
		return nil, err
	}
	claimDisplay(displayClaimConnector, pick.XName)
	if err := forceConnectorPrivileged(statusPath, "on", true); err != nil {
		undo()
		return nil, err
	}
	if got, _ := os.ReadFile(filepath.Join(pick.Dir, "edid")); !isDrawsplayEDID(got) {
		undo()
		return nil, fmt.Errorf("the kernel did not give %s the tablet's EDID (%s)", name, filepath.Join(kwinEDIDFirmwareDir, file))
	}
	k, err := kwinStreamOutput(name, desktopFileInstalled)
	if err != nil {
		undo()
		return nil, err
	}
	return &kwinConnectorOutput{conn: *pick, name: name, k: k}, nil
}

// startKWinOutputStream waits for KWin to show the output called name,
// switching it on when KWin remembers it off, and streams it.
func startKWinOutputStream(name string, desktopFileInstalled bool) (*kwinOutput, error) {
	c, outs, sc, _, err := dialKWinScreencast()
	if errors.Is(err, errKWinNoScreencast) && desktopFileInstalled {
		// KWin reads the new .desktop file once its service cache catches
		// up.
		time.Sleep(kwinRedialDelay)
		c, outs, sc, _, err = dialKWinScreencast()
	}
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(kwinConnectorWait)
	enabled := false
	var outID uint32
	for {
		if id, _, ok := outs.lookup(name); ok {
			outID = id
			break
		}
		if !enabled && time.Until(deadline) < kwinConnectorWait/2 {
			// KWin lists only enabled outputs: the user switched this
			// monitor off last time.
			enabled = true
			if err := kwinEnableOutput(name); err != nil {
				log.Printf("KWin extended display: could not switch %s on: %v", name, err)
			}
		}
		if time.Now().After(deadline) {
			c.Close()
			return nil, fmt.Errorf("KWin did not show %s", name)
		}
		time.Sleep(kwinConnectorPoll)
	}
	k := &kwinOutput{c: c, outs: outs, ready: make(chan error, 1), closed: make(chan struct{})}
	k.stream, err = c.create(k.streamEvent, sc, 0, func(id uint32) []wlArg { // stream_output
		return []wlArg{wlUint(id), wlUint(outID), wlUint(kwinPointerEmbedded)}
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	if err := k.waitReady(); err != nil {
		k.close()
		return nil, err
	}
	return k, nil
}

func (o *kwinConnectorOutput) Name() string             { return o.name }
func (o *kwinConnectorOutput) Changed() <-chan struct{} { return o.k.outs.Changed() }
func (o *kwinConnectorOutput) Closed() <-chan struct{}  { return o.k.closed }
func (o *kwinConnectorOutput) Configure(wlExtConfig) error {
	return errors.New("KWin lays its outputs out itself")
}

// CompositorSized: the user sets the monitor's mode in Display Configuration
// and the host follows it.
func (o *kwinConnectorOutput) CompositorSized() bool { return true }

func (o *kwinConnectorOutput) Target() captureTarget {
	return captureTarget{Kind: capturePipeWire, Node: o.k.node}
}

func (o *kwinConnectorOutput) State() (wlExtState, error) { return o.k.State() }

func (o *kwinConnectorOutput) MapTablet() error {
	noteTouchMapping("KWin", o.name, kwinSetTouchOutput(o.name))
	return kwinSetTabletOutput(o.name)
}

func (o *kwinConnectorOutput) Remove([]outputPos, string) error {
	if o.k.c != nil {
		o.k.close()
	}
	err := releaseKWinPort(o.name, filepath.Join(o.conn.Dir, "status"))
	releaseDisplayClaim(displayClaimConnector, o.conn.XName)
	return err
}

// releaseKWinPort removes the host's EDID override of the port called name,
// then switches the port back to detection, so a monitor plugged in later
// reads its own EDID when the kernel probes it. It never prompts.
func releaseKWinPort(name, statusPath string) error {
	oerr := setEDIDOverride(name, "", false)
	if oerr != nil {
		oerr = fmt.Errorf("could not remove the EDID override of %s: %w", name, oerr)
	}
	if err := forceConnectorPrivileged(statusPath, "detect", false); err != nil {
		return errors.Join(fmt.Errorf("could not switch %s back to detection: %w", name, err), oerr)
	}
	return oerr
}

// connectorNameUnique reports whether no other DRM device has a connector
// called like c (HDMI-A-1): drm.edid_firmware and KWin tell ports apart by
// that name alone.
func connectorNameUnique(c drmHDMIConnector) bool {
	all, _ := filepath.Glob(filepath.Join(sysfsDRMDir, "card*-"+c.Name()))
	for _, dir := range all {
		if m := drmConnectorDir.FindStringSubmatch(filepath.Base(dir)); m != nil && filepath.Clean(dir) != filepath.Clean(c.Dir) {
			return false
		}
	}
	return true
}

func fileWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// rootWithoutPrompt reports whether the host is root or sudo runs without a
// password.
func rootWithoutPrompt() bool {
	if os.Geteuid() == 0 {
		return true
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, sudo, "-n", "true").Run() == nil
}

// inPlugdevGroup reports whether this process is in the plugdev group, which
// the DrawSplay udev rules open the port and drm.edid_firmware to. A group
// joined since login does not count until the next one.
func inPlugdevGroup() bool {
	g, err := user.LookupGroup("plugdev")
	if err != nil {
		return false
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return false
	}
	groups, err := os.Getgroups()
	return err == nil && (os.Getegid() == gid || slices.Contains(groups, gid))
}

// ensureTabletEDIDInstalled makes sure the firmware file holds edid and the
// host may set drm.edid_firmware itself, running the display helper (one
// password prompt) when either is missing.
func ensureTabletEDIDInstalled(file string, edid []byte) error {
	have, _ := os.ReadFile(filepath.Join(kwinEDIDFirmwareDir, file))
	if bytes.Equal(have, edid) && edidParamWritable() {
		return nil
	}
	log.Printf("KWin extended display: installing the tablet's EDID %s (you may be asked for your password)", file)
	_, err := runDisplayHelperFn(true, "edid-install", file, hex.EncodeToString(edid))
	if errors.Is(err, errPasswordPromptDismissed) {
		kwinConnectorRefused = errKWinConnectorDeclined
	}
	if err != nil {
		return fmt.Errorf("installing the tablet's EDID: %w", err)
	}
	return nil
}

func edidParamWritable() bool {
	f, err := os.OpenFile(edidFirmwareParamPath, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// edidOverrideMu serializes changes to drm.edid_firmware.
var edidOverrideMu sync.Mutex

// setEDIDOverride points connector at edid/file in drm.edid_firmware, or with
// file "" removes the host's entry for it. Entries the host did not add are
// kept. It writes the parameter itself when 71-drawsplay-edid-override.rules
// lets it, otherwise through the display helper (mayPrompt: with a password
// prompt).
func setEDIDOverride(connector, file string, mayPrompt bool) error {
	edidOverrideMu.Lock()
	defer edidOverrideMu.Unlock()
	cur, err := os.ReadFile(edidFirmwareParamPath)
	if err != nil {
		if file == "" {
			return nil
		}
		return err
	}
	next := mergeEDIDOverride(string(cur), connector, file)
	if next == normalizeEDIDOverride(string(cur)) {
		return nil
	}
	value := []byte(next)
	if next == "" {
		// A lone NUL empties the parameter; an empty write never reaches
		// the kernel, and a newline would count as a file name.
		value = []byte{0}
	}
	if os.Geteuid() != 0 && edidParamDirectWrite(edidFirmwareParamPath, value) == nil {
		return nil
	}
	arg := file
	if arg == "" {
		arg = "-"
	}
	_, err = runDisplayHelperFn(mayPrompt, "edid-firmware", connector, arg)
	return err
}

// mergeEDIDOverride is drm.edid_firmware value cur with the host's entry for
// connector set to edid/file (first, so it wins over the user's) or, with
// file "", removed. Every other DrawSplay entry goes too: only one port is
// in use at a time, and any other is a leftover.
func mergeEDIDOverride(cur, connector, file string) string {
	var out []string
	if file != "" {
		out = append(out, connector+":edid/"+file)
	}
	for _, e := range strings.Split(normalizeEDIDOverride(cur), ",") {
		if e == "" || isDrawsplayEDIDOverride(e) {
			continue
		}
		out = append(out, e)
	}
	return strings.Join(out, ",")
}

func normalizeEDIDOverride(v string) string {
	return strings.TrimSpace(strings.Trim(v, "\x00"))
}

// isDrawsplayEDIDOverride reports whether a drm.edid_firmware entry is one
// the host added.
func isDrawsplayEDIDOverride(entry string) bool {
	_, file, ok := strings.Cut(entry, ":")
	return ok && strings.HasPrefix(file, "edid/drawsplay-")
}

// releaseKWinConnectorLeftovers runs when the host starts: a previous host
// process that crashed may have left a port forced on with the tablet's
// EDID, and its entry in drm.edid_firmware, which would give a monitor
// plugged into that port the tablet's modes. It never prompts.
func releaseKWinConnectorLeftovers() {
	ports := listHDMIConnectors()
	for _, c := range ports {
		if c.Status == "connected" && c.OwnEDID && displayClaimedByOther(c.XName) {
			// Another host process shows its tablet there.
			return
		}
	}
	// The override goes first, so a released port reads its own EDID.
	removeEDIDOverrideLeftovers()
	for _, c := range ports {
		if c.Status != "connected" || !c.OwnEDID {
			continue
		}
		if err := forceConnectorPrivileged(filepath.Join(c.Dir, "status"), "detect", false); err != nil {
			log.Printf("Could not release %s, left on by an earlier host: %v (run: echo detect | sudo tee %s)", c.Name(), err, filepath.Join(c.Dir, "status"))
			continue
		}
		log.Printf("Released %s, left on by an earlier host", c.Name())
	}
}

// removeEDIDOverrideLeftovers removes every DrawSplay entry from
// drm.edid_firmware, without root. It reports whether none is left.
func removeEDIDOverrideLeftovers() bool {
	edidOverrideMu.Lock()
	defer edidOverrideMu.Unlock()
	cur, err := os.ReadFile(edidFirmwareParamPath)
	if err != nil || !strings.Contains(string(cur), "edid/drawsplay-") {
		return true
	}
	next := mergeEDIDOverride(string(cur), "", "")
	value := []byte(next)
	if next == "" {
		value = []byte{0}
	}
	if err := edidParamDirectWrite(edidFirmwareParamPath, value); err != nil {
		log.Printf("Could not remove DrawSplay's EDID override left by an earlier host (%v); it ends at the next reboot", err)
		return false
	}
	return true
}
