//go:build linux

package main

// Linux virtual display: GNOME Mutter ScreenCast virtual monitor (Wayland)
// with an X11 mirror fallback.
//
// Extend-mode flow (GNOME Wayland, Ubuntu 22.04+):
//
//  1. Connect to the session D-Bus and call
//     org.gnome.Mutter.ScreenCast.CreateSession.
//  2. Call Session.RecordVirtual — Mutter plugs a new virtual monitor into
//     the desktop layout for the lifetime of the session.
//  3. Call Session.Start and wait for the stream's PipeWireStreamAdded
//     signal, which carries the PipeWire node ID the compositor renders the
//     virtual monitor into. That node is the capture target
//     (pipeline_linux.go consumes it with GStreamer's pipewiresrc).
//
// Resolution is OS-managed, not host-configured: RecordVirtual is called
// with an explicit "modes" property (see virtualMonitorModes) listing the
// standard resolutions the web UI used to offer directly. This makes
// GNOME treat the Drawsplay virtual monitor like a real display with a
// selectable mode list in Settings -> Displays, so the user changes
// resolution there instead of in the web UI (which hides its resolution
// control on Linux — see app.js applyResolutionPolicy). The host discovers
// the mode GNOME actually picked via DisplayConfig.GetCurrentState (see
// findVirtualMonitorCurrentMode/currentVirtualMonitorMode) and watches the
// DisplayConfig.MonitorsChanged signal to notice later changes made from
// Settings while a session is running, restarting the session so the
// reconnecting client picks up the new mode.
//
// Fallback: on X11 sessions (or when Mutter's API is unavailable, e.g.
// non-GNOME desktops) the primary X11 screen is mirrored via ffmpeg x11grab
// — same behavior as M1 — so video always works.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	mutterBusName      = "org.gnome.Mutter.ScreenCast"
	mutterObjectPath   = "/org/gnome/Mutter/ScreenCast"
	mutterIface        = "org.gnome.Mutter.ScreenCast"
	mutterSessionIface = "org.gnome.Mutter.ScreenCast.Session"
	mutterStreamIface  = "org.gnome.Mutter.ScreenCast.Stream"

	// displayConfigIface is Mutter's separate DisplayConfig D-Bus interface
	// (distinct from ScreenCast above), used here only to read and watch
	// monitor mode state — see virtualMonitorSerialAndModes and
	// watchVirtualMonitorResolution.
	displayConfigBusName    = "org.gnome.Mutter.DisplayConfig"
	displayConfigObjectPath = "/org/gnome/Mutter/DisplayConfig"
	displayConfigIface      = "org.gnome.Mutter.DisplayConfig"

	pipewireNodeTimeout = 10 * time.Second
)

// virtualMonitorResolutionPresets are the standard resolutions offered to
// GNOME as selectable modes for the Drawsplay virtual monitor (the same set
// the web UI's resolution dropdown used to offer directly — see
// webui/index.html). Declaring them via RecordVirtual's "modes" property is
// what makes the virtual monitor show up in Settings -> Displays with a
// resolution choice instead of a single fixed size.
var virtualMonitorResolutionPresets = []struct{ Width, Height int }{
	{1280, 720},
	{1600, 900},
	{1680, 1050},
	{1920, 1080},
	{2560, 1440},
	{2736, 1824},
	{2880, 1800},
	{3200, 1800},
	{3840, 2160},
}

// virtualMonitorModeSize mirrors RecordVirtual's mode "size" property,
// D-Bus signature "(uu)".
type virtualMonitorModeSize struct {
	Width  uint32
	Height uint32
}

// virtualMonitorModes builds the RecordVirtual "modes" property: the
// standard preset list plus, if the configured default isn't one of them, a
// custom entry for it. Exactly one entry is marked "is-preferred" (the
// configured default, so the initial session matches prior behavior; the
// user then changes it going forward from Settings -> Displays).
func virtualMonitorModes(defaultWidth, defaultHeight int) []map[string]dbus.Variant {
	modes := make([]map[string]dbus.Variant, 0, len(virtualMonitorResolutionPresets)+1)
	preferredSet := false
	for _, p := range virtualMonitorResolutionPresets {
		mode := map[string]dbus.Variant{
			"size": dbus.MakeVariant(virtualMonitorModeSize{uint32(p.Width), uint32(p.Height)}),
		}
		if p.Width == defaultWidth && p.Height == defaultHeight {
			mode["is-preferred"] = dbus.MakeVariant(true)
			preferredSet = true
		}
		modes = append(modes, mode)
	}
	if !preferredSet && defaultWidth > 0 && defaultHeight > 0 {
		modes = append(modes, map[string]dbus.Variant{
			"size":         dbus.MakeVariant(virtualMonitorModeSize{uint32(defaultWidth), uint32(defaultHeight)}),
			"is-preferred": dbus.MakeVariant(true),
		})
	}
	return modes
}

// newVirtualDisplay creates the session display: a Mutter virtual monitor on
// GNOME Wayland, otherwise an x11grab mirror of the primary screen.
//
// The Mutter path is only taken when the GStreamer PipeWire consumer tooling
// is actually runnable: creating the virtual monitor without a way to consume
// its PipeWire stream would leave the session with a monitor but no video
// ("all capture pipelines failed"). Skipping it up front makes the fallback
// deterministic and the reason observable in the log.
func newVirtualDisplay(opts videoOptions) (virtualDisplay, error) {
	if currentLinuxSessionBackend() == "x11" {
		display := os.Getenv("DISPLAY")
		if display == "" {
			return nil, fmt.Errorf("no X11 DISPLAY; cannot capture")
		}
		return &x11MirrorDisplay{display: display}, nil
	}

	toolingErr := cachedGstPipeWireCaptureSupport()
	if toolingErr != nil {
		log.Printf("PipeWire capture tooling unavailable (%v); skipping Mutter virtual monitor", toolingErr)
	} else if d, err := newMutterDisplay(opts); err == nil {
		return d, nil
	} else {
		log.Printf("Mutter virtual monitor unavailable (%v); falling back to X11 mirror", err)
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		if toolingErr != nil {
			return nil, fmt.Errorf("PipeWire capture tooling unavailable (%v) and no X11 DISPLAY for the x11grab fallback; cannot capture", toolingErr)
		}
		return nil, fmt.Errorf("no Mutter ScreenCast API and no X11 DISPLAY; cannot capture")
	}
	if os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		log.Printf("Warning: using x11grab under a Wayland session; only XWayland windows are captured (native Wayland windows appear black). Install the GStreamer PipeWire tooling for full capture (see README).")
	}
	return &x11MirrorDisplay{display: display}, nil
}

func linuxSessionBackend(sessionType, display, waylandDisplay string) string {
	switch strings.ToLower(strings.TrimSpace(sessionType)) {
	case "x11":
		return "x11"
	case "wayland":
		return "wayland"
	}
	if strings.TrimSpace(waylandDisplay) != "" {
		return "wayland"
	}
	if strings.TrimSpace(display) != "" {
		return "x11"
	}
	return "unknown"
}

func currentLinuxSessionBackend() string {
	return linuxSessionBackend(
		os.Getenv("XDG_SESSION_TYPE"),
		os.Getenv("DISPLAY"),
		os.Getenv("WAYLAND_DISPLAY"),
	)
}

// gstPipeWireCaptureSupport reports why GStreamer-based PipeWire capture
// cannot work on this host, or nil when the tooling is runnable. Dependencies
// are injected for testability.
func gstPipeWireCaptureSupport(lookPath func(string) (string, error), inspect func(element string) error) error {
	if _, err := lookPath("gst-launch-1.0"); err != nil {
		return fmt.Errorf("gst-launch-1.0 not found (install gstreamer1.0-tools)")
	}
	if err := inspect("pipewiresrc"); err != nil {
		return fmt.Errorf("GStreamer element pipewiresrc unavailable (install gstreamer1.0-pipewire)")
	}
	return nil
}

// gstInspectElement checks that a GStreamer element is installed.
func gstInspectElement(element string) error {
	return exec.Command("gst-inspect-1.0", element).Run()
}

// cachedGstPipeWireCaptureSupport memoizes gstPipeWireCaptureSupport for the
// life of the host process. The check spawns gst-inspect-1.0 (a process
// fork+exec, tens of milliseconds), and newVirtualDisplay runs it again on
// every session start/reconnect (cable replug, app restart, adaptive
// display recovery). Installed GStreamer plugins don't change while the
// host is running, so the first result is reused, shaving that latency off
// every subsequent session start.
var (
	gstPipeWireCaptureSupportOnce sync.Once
	gstPipeWireCaptureSupportErr  error
)

func cachedGstPipeWireCaptureSupport() error {
	gstPipeWireCaptureSupportOnce.Do(func() {
		gstPipeWireCaptureSupportErr = gstPipeWireCaptureSupport(exec.LookPath, gstInspectElement)
	})
	return gstPipeWireCaptureSupportErr
}

// mutterDisplay is a live Mutter ScreenCast virtual monitor session.
type mutterDisplay struct {
	conn        *dbus.Conn
	sessionPath dbus.ObjectPath
	node        uint32

	// widthMu guards width/height, which start at whatever GNOME picked as
	// the virtual monitor's current mode and can change later if the user
	// picks a different resolution from Settings -> Displays while a
	// session is running (see watchVirtualMonitorResolution).
	widthMu sync.Mutex
	width   int
	height  int

	stopWatch chan struct{}
}

func (d *mutterDisplay) size() (int, int) {
	d.widthMu.Lock()
	defer d.widthMu.Unlock()
	return d.width, d.height
}

func (d *mutterDisplay) setSize(width, height int) {
	d.widthMu.Lock()
	defer d.widthMu.Unlock()
	d.width, d.height = width, height
}

// newMutterDisplay drives the ScreenCast D-Bus API through session creation,
// virtual monitor recording, and PipeWire node discovery.
func newMutterDisplay(opts videoOptions) (*mutterDisplay, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()

	root := conn.Object(mutterBusName, dbus.ObjectPath(mutterObjectPath))
	var sessionPath dbus.ObjectPath
	if err := root.Call(mutterIface+".CreateSession", 0, map[string]dbus.Variant{}).Store(&sessionPath); err != nil {
		return nil, fmt.Errorf("CreateSession: %w", err)
	}
	session := conn.Object(mutterBusName, sessionPath)

	// cursor-mode 1 = embed the real system cursor (whatever shape it
	// currently has) into the captured video, so both physical-mouse and
	// touch/pen movement show the actual host pointer, matching Windows'
	// ddagrab behavior.
	//
	// Known limitation (accepted): Mutter tracks the physical mouse
	// pointer and the Drawsplay Pen uinput tablet-tool device as two
	// independent cursor sprites (a known GNOME/Mutter bug class -- see
	// e.g. GNOME GitLab #3613 and Fedora Discourse "Dual cursors appear
	// on screen"), so both can appear composited into the frame at once:
	// one tracking the real mouse and a separate one left over from the
	// last touch/pen event. A from-scratch Android-side overlay was
	// tried to work around this but was reverted -- it can only reflect
	// touch/pen position (the host has no way to observe the real mouse
	// pointer without extra OS-level polling), so it went invisible
	// during physical-mouse use, which is worse. Embedded mode with the
	// occasional dual-cursor artifact is the accepted tradeoff.
	// modes declares a fixed, selectable mode list instead of leaving the
	// stream freely resizable to whatever the PipeWire consumer negotiates
	// (Mutter's default). That default is what made the web UI the only
	// place resolution could be changed; declaring modes here is what
	// makes the virtual monitor show up with a real resolution dropdown
	// in Settings -> Displays, matching a physical monitor's behavior.
	recordProps := map[string]dbus.Variant{
		"cursor-mode": dbus.MakeVariant(uint32(1)),
		"modes":       dbus.MakeVariant(virtualMonitorModes(opts.Width, opts.Height)),
	}
	var streamPath dbus.ObjectPath
	if err := session.Call(mutterSessionIface+".RecordVirtual", 0, recordProps).Store(&streamPath); err != nil {
		return nil, fmt.Errorf("RecordVirtual: %w", err)
	}

	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(streamPath),
		dbus.WithMatchInterface(mutterStreamIface),
		dbus.WithMatchMember("PipeWireStreamAdded"),
	); err != nil {
		return nil, fmt.Errorf("signal match: %w", err)
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)

	if err := session.Call(mutterSessionIface+".Start", 0).Err; err != nil {
		return nil, fmt.Errorf("session start: %w", err)
	}

	node, err := waitForPipeWireNode(signals, streamPath)
	if err != nil {
		_ = session.Call(mutterSessionIface+".Stop", 0).Err
		return nil, err
	}
	// Nothing else is read from signals; unregister it so godbus's
	// per-channel delivery queue (see watchVirtualMonitorResolution below
	// for the channel that IS read going forward) doesn't grow unbounded
	// once MonitorsChanged starts matching on this connection too.
	conn.RemoveSignal(signals)

	// Best-effort: keep GNOME's Wacom "Map to Monitor" setting for the
	// Drawsplay Pen pointed at this session's virtual monitor. Mutter gives
	// every RecordVirtual call a fresh connector serial, so a mapping set in
	// a previous session goes stale and touch/pen silently falls back to the
	// physical display even though video keeps streaming correctly — this
	// closes that gap without requiring the user to redo it in Settings
	// every reconnect.
	go remapTabletInputToVirtualMonitor()

	// GNOME picks the actual starting mode from the "modes" list above
	// (the one marked is-preferred, normally matching opts.Width/Height,
	// unless the user already changed the resolution in Settings ->
	// Displays for a previous session and GNOME retained that choice).
	// Query it rather than assuming opts.Width/Height so the downstream
	// pipeline caps always match what Mutter is actually compositing.
	width, height, ok2 := currentVirtualMonitorMode(10, 150*time.Millisecond)
	if !ok2 {
		log.Printf("Could not read virtual monitor's current mode from GNOME; falling back to configured %dx%d", opts.Width, opts.Height)
		width, height = opts.Width, opts.Height
	}

	ok = true
	d := &mutterDisplay{
		conn:        conn,
		sessionPath: sessionPath,
		node:        node,
		width:       width,
		height:      height,
		stopWatch:   make(chan struct{}),
	}
	if err := d.watchResolution(); err != nil {
		// Best-effort: a live GNOME Settings resolution change simply
		// won't trigger an automatic reconnect if this fails to
		// subscribe; the session itself is otherwise fully functional.
		log.Printf("Virtual monitor resolution watch disabled: %v", err)
	}
	return d, nil
}

// waitForPipeWireNode blocks until the stream announces its PipeWire node.
func waitForPipeWireNode(signals chan *dbus.Signal, streamPath dbus.ObjectPath) (uint32, error) {
	timeout := time.After(pipewireNodeTimeout)
	for {
		select {
		case sig := <-signals:
			if sig == nil {
				return 0, fmt.Errorf("D-Bus signal channel closed")
			}
			if sig.Path == streamPath && sig.Name == mutterStreamIface+".PipeWireStreamAdded" && len(sig.Body) == 1 {
				if node, isU32 := sig.Body[0].(uint32); isU32 {
					return node, nil
				}
			}
		case <-timeout:
			return 0, fmt.Errorf("no PipeWireStreamAdded signal within %s", pipewireNodeTimeout)
		}
	}
}

// virtualMonitorConnectorRe matches a Mutter DisplayConfig connector tuple
// for the ephemeral virtual monitor a ScreenCast RecordVirtual call creates
// -- e.g. ('Meta-0', 'MetaVendor', 'Virtual remote monitor', '0x0001fc') in
// GetCurrentState's gdbus-call text output. Capture group 1 is the serial,
// the part of the tuple that changes every time Mutter recreates the
// monitor and is what GNOME's per-tablet "output" mapping is keyed on.
var virtualMonitorConnectorRe = regexp.MustCompile(
	`\('[^']*', 'MetaVendor', 'Virtual remote monitor', '([^']*)'\)`)

// findVirtualMonitorSerial extracts the current virtual monitor's connector
// serial from GetCurrentState's text output, or "" if none is found.
func findVirtualMonitorSerial(getCurrentStateOutput string) string {
	m := virtualMonitorConnectorRe.FindStringSubmatch(getCurrentStateOutput)
	if m == nil {
		return ""
	}
	return m[1]
}

// virtualMonitorCurrentModeRe matches a single mode tuple within
// GetCurrentState's text output that is flagged as the currently active
// mode, e.g.:
//
//	('1920x1080@60.00', 1920, 1080, 60.0, 1.0, [1.0], {'is-current': <true>})
//
// capturing width and height. A mode's own trailing properties dict never
// nests braces in practice (values are scalars/arrays, not dicts), so
// matching up to the first "}" is unambiguous -- unlike the modes *list*
// as a whole, which does contain nested "[...]" (each mode's supported
// scales) and can't be bounded this simply (see queryMutterDisplayState's
// caller, which scopes this regex to only the substring following the
// virtual monitor's own connector tuple).
var virtualMonitorCurrentModeRe = regexp.MustCompile(
	`\('[^']*', (\d+), (\d+), [0-9.]+, [0-9.]+, \[[^\]]*\], \{[^}]*'is-current': <true>[^}]*\}\)`)

// findVirtualMonitorCurrentMode extracts the width/height of the virtual
// monitor's currently active mode from GetCurrentState's text output.
// Returns ok=false if no virtual monitor connector, or no mode flagged
// is-current within it, is found.
func findVirtualMonitorCurrentMode(getCurrentStateOutput string) (width, height int, ok bool) {
	loc := virtualMonitorConnectorRe.FindStringIndex(getCurrentStateOutput)
	if loc == nil {
		return 0, 0, false
	}
	rest := getCurrentStateOutput[loc[1]:]
	// Bound the search to just this connector's entry, in case a future
	// GNOME version ever lists more than one virtual monitor -- without
	// this, the regex below could match a later connector's current mode
	// instead.
	if nextLoc := virtualMonitorConnectorRe.FindStringIndex(rest); nextLoc != nil {
		rest = rest[:nextLoc[0]]
	}
	m := virtualMonitorCurrentModeRe.FindStringSubmatch(rest)
	if m == nil {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(m[1])
	h, errH := strconv.Atoi(m[2])
	if errW != nil || errH != nil {
		return 0, 0, false
	}
	return w, h, true
}

// queryMutterDisplayState runs GetCurrentState via gdbus, the same call
// convention already used by remapTabletInputToVirtualMonitor.
func queryMutterDisplayState() (string, error) {
	out, err := exec.Command("gdbus", "call", "--session",
		"--dest", displayConfigBusName,
		"--object-path", displayConfigObjectPath,
		"--method", displayConfigIface+".GetCurrentState").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("GetCurrentState: %w: %s", err, bytes.TrimSpace(out))
	}
	return string(out), nil
}

// currentVirtualMonitorMode queries GNOME for the virtual monitor's actual
// current mode, retrying briefly since a fresh RecordVirtual call's
// DisplayConfig layout registration can lag a bit behind the
// PipeWireStreamAdded signal (same race remapTabletInputToVirtualMonitor
// already retries around).
func currentVirtualMonitorMode(maxAttempts int, retryDelay time.Duration) (width, height int, ok bool) {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, err := queryMutterDisplayState()
		if err == nil {
			if w, h, found := findVirtualMonitorCurrentMode(out); found {
				return w, h, true
			}
		}
		if attempt < maxAttempts {
			time.Sleep(retryDelay)
		}
	}
	return 0, 0, false
}

// watchResolution subscribes to Mutter's DisplayConfig.MonitorsChanged
// signal and restarts the active video session when the virtual monitor's
// current mode actually changes -- i.e. when the user picks a different
// resolution from Settings -> Displays while a session is running. Reuses
// the existing, already-exercised restartActiveVideoSession() (used by the
// web UI's settings-update handler) rather than adding a second live
// mode-change path into video.go's pipeline loop: the reconnecting client
// picks a fresh mutterDisplay whose starting width/height come from
// currentVirtualMonitorMode, so it naturally follows GNOME's new choice.
func (d *mutterDisplay) watchResolution() error {
	if err := d.conn.AddMatchSignal(
		dbus.WithMatchInterface(displayConfigIface),
		dbus.WithMatchMember("MonitorsChanged"),
	); err != nil {
		return fmt.Errorf("MonitorsChanged match: %w", err)
	}
	changes := make(chan *dbus.Signal, 8)
	d.conn.Signal(changes)

	go func() {
		defer d.conn.RemoveSignal(changes)
		// debounceTimer coalesces bursts of MonitorsChanged (GNOME can fire
		// several while applying one resolution change) into a single
		// check, run shortly after the last signal in a burst rather than
		// on every individual signal.
		const debounce = 400 * time.Millisecond
		var debounceTimer *time.Timer
		var debounceC <-chan time.Time
		for {
			select {
			case <-d.stopWatch:
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				return
			case sig, chOK := <-changes:
				if !chOK {
					return
				}
				if sig.Name != displayConfigIface+".MonitorsChanged" {
					continue
				}
				if debounceTimer == nil {
					debounceTimer = time.NewTimer(debounce)
				} else {
					debounceTimer.Reset(debounce)
				}
				debounceC = debounceTimer.C
			case <-debounceC:
				debounceC = nil
				w, h, found := currentVirtualMonitorMode(3, 100*time.Millisecond)
				if !found {
					continue
				}
				curW, curH := d.size()
				if w == curW && h == curH {
					continue
				}
				log.Printf("Virtual monitor resolution changed in GNOME Settings (%dx%d -> %dx%d); restarting video session", curW, curH, w, h)
				d.setSize(w, h)
				// Persist the new resolution as the configured default so
				// the next session's RecordVirtual call marks it (not the
				// stale old default) "is-preferred" -- otherwise every
				// restart would immediately snap back to whatever
				// resolution was configured when the host started.
				if globalSettingsStore != nil {
					if err := globalSettingsStore.updateResolution(w, h); err != nil {
						log.Printf("Could not persist GNOME-selected resolution %dx%d: %v", w, h, err)
					}
				}
				restartActiveVideoSession()
			}
		}
	}()
	return nil
}

// remapTabletInputToVirtualMonitor points GNOME's Wacom "Map to Monitor"
// setting for the Drawsplay Pen at whichever virtual monitor this session
// just created, so touch/pen input follows the extended desktop instead of
// silently falling back to the physical display when a previous session's
// mapping (keyed by connector serial) goes stale.
//
// Retries briefly: Mutter's DisplayConfig layout registration for the new
// virtual monitor can lag a few tens of milliseconds behind the
// PipeWireStreamAdded signal that unblocks the caller, so an immediate
// GetCurrentState call can race it and see the monitor list without the
// new connector yet.
//
// Best-effort only: gdbus/gsettings are standard on any GNOME desktop (a
// hard requirement for the virtual-monitor capture path itself already),
// but a failure here must never fail display setup -- worst case the user
// falls back to the manual Settings -> Wacom Tablet -> Map to Monitor step
// documented in inject_linux.go.
func remapTabletInputToVirtualMonitor() {
	const maxAttempts = 10
	const retryDelay = 150 * time.Millisecond

	var serial string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, err := queryMutterDisplayState()
		if err != nil {
			log.Printf("Tablet-to-monitor remap skipped: %v", err)
			return
		}
		if serial = findVirtualMonitorSerial(out); serial != "" {
			break
		}
		if attempt < maxAttempts {
			time.Sleep(retryDelay)
		}
	}
	if serial == "" {
		log.Printf("Tablet-to-monitor remap skipped: no virtual monitor connector in display state after %d attempts", maxAttempts)
		return
	}
	dconfKey := fmt.Sprintf("%x:%x", drawsplayPenVendorID, drawsplayPenProductID)
	schemaPath := fmt.Sprintf("org.gnome.desktop.peripherals.tablet:/org/gnome/desktop/peripherals/tablets/%s/", dconfKey)
	value := fmt.Sprintf("['MetaVendor', 'Virtual remote monitor', '%s']", serial)
	if err := exec.Command("gsettings", "set", schemaPath, "output", value).Run(); err != nil {
		log.Printf("Tablet-to-monitor remap skipped: gsettings set failed: %v", err)
		return
	}
	log.Printf("Mapped Drawsplay Pen touch/pen input to virtual monitor (serial %s)", serial)
}

func (d *mutterDisplay) Target() captureTarget {
	width, height := d.size()
	return captureTarget{
		Kind:   capturePipeWire,
		Node:   d.node,
		Width:  width,
		Height: height,
		// Mutter places the virtual monitor in the layout itself; its
		// position is not exposed here, so Rect stays zero and input
		// injection relies on the uinput device's own mapping (see
		// inject_linux.go).
	}
}

func (d *mutterDisplay) Description() string {
	width, height := d.size()
	return fmt.Sprintf("Mutter virtual monitor (PipeWire node %d, %dx%d)", d.node, width, height)
}

// Refresh is a no-op: the PipeWire node ID is stable for the session's
// lifetime and Rect is unused on this path (see Target). Width/height are
// updated independently by watchResolution when GNOME reports a mode
// change; a genuine change triggers a full session restart rather than a
// live Refresh, so callers don't need to poll this to observe it.
func (d *mutterDisplay) Refresh() (captureTarget, error) { return d.Target(), nil }

func (d *mutterDisplay) Close() {
	if d.stopWatch != nil {
		close(d.stopWatch)
	}
	session := d.conn.Object(mutterBusName, d.sessionPath)
	_ = session.Call(mutterSessionIface+".Stop", 0).Err
	_ = d.conn.Close()
}

// x11MirrorDisplay mirrors the X11 root screen (M1 behavior).
type x11MirrorDisplay struct {
	display string
}

func (d *x11MirrorDisplay) Target() captureTarget {
	return captureTarget{Kind: captureX11Grab}
}

func (d *x11MirrorDisplay) Description() string {
	return fmt.Sprintf("mirror of X11 display %s", d.display)
}

// Refresh is a no-op: x11grab always records the root screen.
func (d *x11MirrorDisplay) Refresh() (captureTarget, error) { return d.Target(), nil }

func (d *x11MirrorDisplay) Close() {}
