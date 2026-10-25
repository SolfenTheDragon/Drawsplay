//go:build windows

package main

// Windows virtual display: DrawSplay's own IddCx driver (extend mode) with a
// mirror fallback.
//
// Extend-mode flow:
//
//  1. Snapshot the current DXGI outputs (dxgi_windows.go).
//  2. Open the DrawSplay IddCx control device and plug one monitor at the
//     tablet's native resolution and refresh rate (idd_windows.go). Windows
//     enumerates a new monitor within ~1 s.
//  3. Poll DXGI until an output appears that was not in the snapshot; its
//     (adapter, output) indices become the ddagrab capture target and its
//     desktop rectangle becomes the input-mapping rect.
//
// If the driver is not installed, newVirtualDisplay logs a hint and degrades
// to mirroring the primary attached output, so video always works.
//
// The driver creates the monitor at exactly the requested mode (its only
// advertised mode, marked preferred), so unlike the previous Parsec VDD
// integration there is no custom-resolution registry key, no adapter reload
// and no keepalive: closing the control handle removes the monitor.

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	vddOutputPollInterval = 200 * time.Millisecond
	vddOutputPollTimeout  = 10 * time.Second

	// SetDisplayConfig flags (winuser.h).
	sdcTopologyExtend = 0x00000004
	sdcApply          = 0x00000080
)

var procSetDisplayConfig = user32.NewProc("SetDisplayConfig")

const vddCaptureFallbackCooldown = 5 * time.Minute

var vddCaptureFallbackState struct {
	sync.Mutex
	until    time.Time
	baseline map[string]bool
}

func markVDDCaptureFallback(now time.Time, baseline map[string]bool) {
	vddCaptureFallbackState.Lock()
	vddCaptureFallbackState.until = now.Add(vddCaptureFallbackCooldown)
	vddCaptureFallbackState.baseline = make(map[string]bool, len(baseline))
	for name := range baseline {
		vddCaptureFallbackState.baseline[name] = true
	}
	vddCaptureFallbackState.Unlock()
}

func vddCaptureFallbackActive(now time.Time) (time.Time, map[string]bool, bool) {
	vddCaptureFallbackState.Lock()
	defer vddCaptureFallbackState.Unlock()
	if !now.Before(vddCaptureFallbackState.until) {
		vddCaptureFallbackState.until = time.Time{}
		vddCaptureFallbackState.baseline = nil
		return time.Time{}, nil, false
	}
	baseline := make(map[string]bool, len(vddCaptureFallbackState.baseline))
	for name := range vddCaptureFallbackState.baseline {
		baseline[name] = true
	}
	return vddCaptureFallbackState.until, baseline, true
}

// desiredDisplayMode is the mode the session's virtual monitor is plugged at
// and the tablet's native size, which the driver always offers alongside the
// standard 720p-4K modes in Windows Display Settings.
//
// The initial mode is the one the user last picked in Windows Display
// Settings for this tablet (rememberedWindowsDisplayMode), else the tablet's
// native resolution. The tablet's remembered mode (modePrefs) and the host
// setting are not used: a stale remembered mode (e.g. 1920x1080 stored during
// the former Parsec integration) would otherwise stick forever. Clients that
// report no native size use the host-configured size (opts), then 1080p. The
// refresh rate defaults to the tablet panel (tabletPanelHz, 120 Hz when none
// reported).
func desiredDisplayMode(opts videoOptions) (w, h, hz, nativeW, nativeH int) {
	ident := activeClientIdentity()
	if ident.NativeWidth > 0 && ident.NativeHeight > 0 {
		nativeW, nativeH = ident.NativeWidth, ident.NativeHeight
		w, h = nativeW, nativeH
		if rw, rh, ok := rememberedWindowsDisplayMode(nativeW, nativeH); ok {
			w, h = rw, rh
		}
	} else {
		w, h = opts.Width, opts.Height
	}
	if w <= 0 || h <= 0 {
		w, h = 1920, 1080
	}
	if nativeW <= 0 || nativeH <= 0 {
		nativeW, nativeH = w, h
	}
	hz = tabletPanelHz()
	if hz <= 0 {
		hz = 60
	}
	return w, h, hz, nativeW, nativeH
}

// newVirtualDisplay creates the session display: a DrawSplay IddCx virtual
// monitor when available, then a Parsec VDD monitor, then primary mirroring.
//
// Plugging the monitor connects it but Windows may leave it inactive (not
// part of the desktop) — observed on Windows 11 where the OS remembers
// monitor topology. If the new output has not appeared shortly after the
// ADD, the extend topology is re-applied via SetDisplayConfig, which
// activates every connected monitor.
func newVirtualDisplay(opts videoOptions) (virtualDisplay, error) {
	before, err := enumerateOutputs()
	if err != nil {
		return nil, fmt.Errorf("DXGI enumeration: %w", err)
	}
	if until, baseline, active := vddCaptureFallbackActive(time.Now()); active {
		log.Printf("Skipping virtual display after repeated capture loss; using mirror mode until %s", until.Format(time.RFC3339))
		return newMirrorDisplayForBaseline(before, baseline)
	}

	return firstWindowsDisplay(
		func() (virtualDisplay, error) { return newDriverDisplay(opts, before, false) },
		func() (virtualDisplay, error) { return newDriverDisplay(opts, before, true) },
		func() (virtualDisplay, error) { return newMirrorDisplay(before) },
	)
}

func firstWindowsDisplay(candidates ...func() (virtualDisplay, error)) (virtualDisplay, error) {
	var failures []error
	for _, candidate := range candidates {
		display, err := candidate()
		if err == nil {
			return display, nil
		}
		failures = append(failures, err)
		log.Printf("Windows display backend unavailable; trying next fallback: %v", err)
	}
	return nil, errors.Join(failures...)
}

type monitorDevice interface {
	AddMonitor(int, int, int, int, int) (int, error)
	RemoveMonitor(int) error
	Close()
}

func openMonitorDevice(parsec bool) (monitorDevice, error) {
	if parsec {
		return openParsecDevice()
	}
	return openIDDDevice()
}

func newDriverDisplay(opts videoOptions, before []displayOutput, parsec bool) (virtualDisplay, error) {
	dev, err := openMonitorDevice(parsec)
	if err != nil {
		return nil, err
	}

	w, h, hz, nativeW, nativeH := desiredDisplayMode(opts)
	knownOutputs := attachedOutputNames(before)
	monitorID, out, err := plugIDDMonitor(dev, w, h, hz, nativeW, nativeH, knownOutputs)
	if err != nil {
		dev.Close()
		waitForVDDOutputGone(knownOutputs, 3*time.Second)
		return nil, err
	}

	log.Printf("Virtual display up: %s at %dx%d (requested %dx%d @ %d Hz; Parsec fallback=%v)",
		out.DeviceName, out.Rect.Right-out.Rect.Left, out.Rect.Bottom-out.Rect.Top, w, h, hz, parsec)

	// Baseline user-change detection on the mode actually active.
	if aw, ah := int(out.Rect.Right-out.Rect.Left), int(out.Rect.Bottom-out.Rect.Top); aw > 0 && ah > 0 && (aw >= ah) == (w >= h) {
		w, h = aw, ah
	}

	return &iddDisplay{
		dev:          dev,
		monitorID:    monitorID,
		output:       out,
		knownOutputs: knownOutputs,
		width:        w,
		height:       h,
		refreshHz:    hz,
		nativeWidth:  nativeW,
		nativeHeight: nativeH,
		parsec:       parsec,
	}, nil
}

// plugIDDMonitor adds one virtual monitor starting at w x h @ hz (also
// offering the native size and the standard modes) and waits for its
// DXGI output to become attached, forcing the extend topology if Windows
// leaves the new monitor inactive. knownOutputs is the set of GDI device
// names attached before this session; the new output is the one not in it.
func plugIDDMonitor(dev monitorDevice, w, h, hz, nativeW, nativeH int, knownOutputs map[string]bool) (int, displayOutput, error) {
	monitorID, err := dev.AddMonitor(w, h, hz, nativeW, nativeH)
	if err != nil {
		return -1, displayOutput{}, err
	}

	// Give Windows a moment to auto-activate the monitor; if it stays
	// inactive, force the extend topology and wait again.
	out, err := waitForNewOutput(knownOutputs, 3*time.Second)
	if err != nil {
		log.Printf("Virtual display connected but inactive; applying extend topology")
		ret, _, _ := procSetDisplayConfig.Call(0, 0, 0, 0, sdcTopologyExtend|sdcApply)
		if ret != 0 {
			log.Printf("SetDisplayConfig(extend) failed: %d", ret)
		}
		out, err = waitForNewOutput(knownOutputs, vddOutputPollTimeout)
	}
	if err != nil {
		if removeErr := dev.RemoveMonitor(monitorID); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
		return -1, displayOutput{}, err
	}
	return monitorID, applyPluggedMode(out, w, h, hz), nil
}

// applyPluggedMode switches a just-plugged monitor to the requested mode when
// Windows activated it at another advertised one, and returns the output
// with its updated rect. Failure is logged and the current mode is kept.
func applyPluggedMode(out displayOutput, w, h, hz int) displayOutput {
	r := out.Rect
	if int(r.Right-r.Left) == w && int(r.Bottom-r.Top) == h {
		return out
	}
	log.Printf("Windows activated %s at %dx%d; applying requested %dx%d @ %d Hz",
		out.DeviceName, r.Right-r.Left, r.Bottom-r.Top, w, h, hz)
	if err := setModeOf(out.DeviceName, w, h, hz); err != nil {
		log.Printf("Could not apply requested mode: %v", err)
		return out
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if outputs, err := enumerateOutputs(); err == nil {
			for _, o := range outputs {
				if o.Attached && o.DeviceName == out.DeviceName {
					out = o
					if int(o.Rect.Right-o.Rect.Left) == w && int(o.Rect.Bottom-o.Rect.Top) == h {
						return out
					}
				}
			}
		}
		time.Sleep(vddOutputPollInterval)
	}
	return out
}

// waitForNewOutput polls DXGI until an attached output not present in the
// known-name set shows up (the just-plugged virtual monitor).
func waitForNewOutput(known map[string]bool, timeout time.Duration) (displayOutput, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		outputs, err := enumerateOutputs()
		if err == nil {
			for _, o := range outputs {
				if o.Attached && !known[o.DeviceName] {
					return o, nil
				}
			}
		}
		time.Sleep(vddOutputPollInterval)
	}
	return displayOutput{}, fmt.Errorf("no new display output within %s", timeout)
}

// targetFor converts an enumerated output into a ddagrab capture target.
func targetFor(o displayOutput) captureTarget {
	r := o.Rect
	return captureTarget{
		Kind:          captureDDAGrab,
		Adapter:       o.AdapterIndex,
		Output:        o.OutputIndex,
		AdapterVendor: o.VendorID,
		DeviceName:    o.DeviceName,
		Width:         int(r.Right - r.Left),
		Height:        int(r.Bottom - r.Top),
		Rect: displayRect{
			X: int(r.Left),
			Y: int(r.Top),
			W: int(r.Right - r.Left),
			H: int(r.Bottom - r.Top),
		},
	}
}

func attachedOutputNames(outputs []displayOutput) map[string]bool {
	names := make(map[string]bool, len(outputs))
	for _, output := range outputs {
		if output.Attached {
			names[output.DeviceName] = true
		}
	}
	return names
}

// resolveVDDOutput first follows the current GDI name, then falls back to
// the single attached output that was not present before AddMonitor. This
// handles DISPLAY5 -> DISPLAY22 churn without guessing when multiple new
// monitors appeared concurrently.
func resolveVDDOutput(current displayOutput, knownOutputs map[string]bool, outputs []displayOutput) (displayOutput, error) {
	var replacements []displayOutput
	for _, output := range outputs {
		if !output.Attached {
			continue
		}
		if output.DeviceName == current.DeviceName {
			return output, nil
		}
		if !knownOutputs[output.DeviceName] {
			replacements = append(replacements, output)
		}
	}
	if len(replacements) == 1 {
		return replacements[0], nil
	}
	if len(replacements) == 0 {
		return displayOutput{}, fmt.Errorf("display %s is not currently attached", current.DeviceName)
	}
	return displayOutput{}, fmt.Errorf(
		"display %s disappeared and %d replacement outputs are ambiguous",
		current.DeviceName, len(replacements),
	)
}

// iddDisplay is a live DrawSplay IddCx virtual monitor.
type iddDisplay struct {
	dev          monitorDevice
	monitorID    int
	output       displayOutput
	knownOutputs map[string]bool
	width        int
	height       int
	refreshHz    int
	nativeWidth  int
	nativeHeight int
	parsec       bool
}

func (d *iddDisplay) Target() captureTarget { return targetFor(d.output) }

// Refresh re-locates the virtual monitor by GDI name or, after a name churn,
// by the output added for this session. A size change since the last refresh
// is a mode the user picked in Windows Display Settings: it is remembered so
// the next session plugs the monitor at that mode.
func (d *iddDisplay) Refresh() (captureTarget, error) {
	outputs, err := enumerateOutputs()
	if err != nil {
		return captureTarget{}, fmt.Errorf("DXGI enumeration: %w", err)
	}
	resolved, err := resolveVDDOutput(d.output, d.knownOutputs, outputs)
	if err != nil {
		return captureTarget{}, err
	}
	if resolved.DeviceName != d.output.DeviceName ||
		resolved.AdapterIndex != d.output.AdapterIndex ||
		resolved.OutputIndex != d.output.OutputIndex {
		log.Printf(
			"Virtual display capture target changed: %s adapter %d output %d -> %s adapter %d output %d",
			d.output.DeviceName, d.output.AdapterIndex, d.output.OutputIndex,
			resolved.DeviceName, resolved.AdapterIndex, resolved.OutputIndex,
		)
	}
	d.output = resolved
	d.noteUserMode(targetFor(d.output))
	return targetFor(d.output), nil
}

// noteUserMode remembers a resolution the user switched the virtual monitor
// to and makes recovery re-plug at it.
func (d *iddDisplay) noteUserMode(t captureTarget) {
	if t.Width <= 0 || t.Height <= 0 || (t.Width == d.width && t.Height == d.height) {
		return
	}
	// A rotated display swaps the reported size; that is an orientation,
	// not a resolution choice.
	if (t.Width >= t.Height) != (d.nativeWidth >= d.nativeHeight) {
		return
	}
	d.width, d.height = t.Width, t.Height
	if err := rememberWindowsDisplayMode(d.nativeWidth, d.nativeHeight, t.Width, t.Height); err != nil {
		log.Printf("Could not remember Windows display mode %dx%d: %v", t.Width, t.Height, err)
		return
	}
	log.Printf("Remembered Windows display mode %dx%d for tablet %dx%d", t.Width, t.Height, d.nativeWidth, d.nativeHeight)
}

func (d *iddDisplay) Description() string {
	if d.parsec {
		return fmt.Sprintf("virtual display %s (Parsec VDD fallback #%d, adapter %d output %d, %dx%d)",
			d.output.DeviceName, d.monitorID, d.output.AdapterIndex, d.output.OutputIndex,
			int(d.output.Rect.Right-d.output.Rect.Left), int(d.output.Rect.Bottom-d.output.Rect.Top))
	}
	return fmt.Sprintf("virtual display %s (DrawSplay IDD #%d, adapter %d output %d, %dx%d @ %d Hz)",
		d.output.DeviceName, d.monitorID, d.output.AdapterIndex, d.output.OutputIndex,
		int(d.output.Rect.Right-d.output.Rect.Left), int(d.output.Rect.Bottom-d.output.Rect.Top), d.refreshHz)
}

// Recover re-plugs the virtual monitor after repeated capture failures. The
// driver tears down the old monitor when the control handle closes, so the
// recovery is a clean open/add cycle.
func (d *iddDisplay) Recover() error {
	d.Close()

	newDev, err := reopenMonitorDevice(d.parsec, 10*time.Second)
	if err != nil {
		return fmt.Errorf("reopen virtual-display control device (Parsec fallback=%v): %w", d.parsec, err)
	}
	monitorID, out, err := plugIDDMonitor(newDev, d.width, d.height, d.refreshHz, d.nativeWidth, d.nativeHeight, d.knownOutputs)
	if err != nil {
		newDev.Close()
		return fmt.Errorf("replug virtual display after recovery: %w", err)
	}
	d.dev, d.monitorID, d.output = newDev, monitorID, out
	log.Printf("Recovered virtual display as %s (Parsec fallback=%v)", d.output.DeviceName, d.parsec)
	return nil
}

// FallbackToMirror tears down an unstable virtual display and mirrors a
// display that was attached before this session. The cooldown prevents a
// reconnecting client from immediately recreating the same broken display.
func (d *iddDisplay) FallbackToMirror() (virtualDisplay, error) {
	d.Close()
	markVDDCaptureFallback(time.Now(), d.knownOutputs)
	outputs, err := enumerateOutputs()
	if err != nil {
		return nil, fmt.Errorf("enumerate displays after removing unstable virtual display: %w", err)
	}
	mirror, err := newMirrorDisplayForBaseline(outputs, d.knownOutputs)
	if err != nil {
		return nil, fmt.Errorf("select an attached display for mirror fallback: %w", err)
	}
	return mirror, nil
}

// Close removes the virtual display and waits briefly for Windows to actually
// detach the monitor. The next session's newVirtualDisplay diffs a "before"
// snapshot against post-AddMonitor enumeration; returning while the old
// monitor is still attached (or its GDI device name still in flux) makes that
// diff misidentify the new output. Waiting here keeps handoffs deterministic:
// one display out, then one display in.
func (d *iddDisplay) Close() {
	if d.dev == nil {
		return
	}
	if err := d.dev.RemoveMonitor(d.monitorID); err != nil {
		log.Printf("Remove session virtual display (Parsec fallback=%v): %v", d.parsec, err)
	}
	waitForVDDOutputGone(d.knownOutputs, 3*time.Second)
	d.dev.Close()
	d.dev = nil
}

// reopenIDDDevice retries opening the control device while the driver
// finishes re-enumerating after a monitor teardown.
func reopenIDDDevice(timeout time.Duration) (*iddDevice, error) {
	deadline := time.Now().Add(timeout)
	for {
		dev, err := openIDDDevice()
		if err == nil {
			return dev, nil
		}

		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func reopenMonitorDevice(parsec bool, timeout time.Duration) (monitorDevice, error) {
	if !parsec {
		return reopenIDDDevice(timeout)
	}
	deadline := time.Now().Add(timeout)
	for {
		dev, err := openParsecDevice()
		if err == nil {
			return dev, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitForVDDOutputGone polls until every attached output is from the
// pre-session baseline. This remains correct if Windows renamed the virtual
// output while the session was active.
func waitForVDDOutputGone(knownOutputs map[string]bool, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		outputs, err := enumerateOutputs()
		if err == nil {
			present := false
			for _, o := range outputs {
				if o.Attached && !knownOutputs[o.DeviceName] {
					present = true
					break
				}
			}
			if !present {
				return
			}
		}
		time.Sleep(vddOutputPollInterval)
	}
	log.Printf("Virtual display still attached after %s; continuing teardown", timeout)
}

// mirrorDisplay mirrors the primary attached output (M1 behavior) when the
// virtual-display driver is unavailable.
type mirrorDisplay struct {
	output displayOutput
}

// newMirrorDisplay picks the primary attached output — the one whose desktop
// rectangle starts at (0,0) — or the first attached output as a last resort.
func newMirrorDisplay(outputs []displayOutput) (virtualDisplay, error) {
	return newMirrorDisplayForBaseline(outputs, nil)
}

func newMirrorDisplayForBaseline(outputs []displayOutput, baseline map[string]bool) (virtualDisplay, error) {
	var chosen *displayOutput
	for i := range outputs {
		o := &outputs[i]
		if !o.Attached {
			continue
		}
		if baseline != nil && !baseline[o.DeviceName] {
			continue
		}
		if chosen == nil || (o.Rect.Left == 0 && o.Rect.Top == 0) {
			chosen = o
			if o.Rect.Left == 0 && o.Rect.Top == 0 {
				break
			}
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("no attached display output found")
	}
	return &mirrorDisplay{output: *chosen}, nil
}

func (d *mirrorDisplay) Target() captureTarget {
	target := targetFor(d.output)
	target.Mirrored = true
	return target
}

// Refresh re-picks the primary output; rearranging monitors can change
// which output sits at (0,0) and every output's rect and index.
func (d *mirrorDisplay) Refresh() (captureTarget, error) {
	outputs, err := enumerateOutputs()
	if err != nil {
		return captureTarget{}, fmt.Errorf("DXGI enumeration: %w", err)
	}
	nd, err := newMirrorDisplay(outputs)
	if err != nil {
		return captureTarget{}, err
	}
	d.output = nd.(*mirrorDisplay).output
	return d.Target(), nil
}

func (d *mirrorDisplay) Description() string {
	return fmt.Sprintf("mirror of %s (adapter %d output %d, %dx%d)",
		d.output.DeviceName, d.output.AdapterIndex, d.output.OutputIndex,
		int(d.output.Rect.Right-d.output.Rect.Left), int(d.output.Rect.Bottom-d.output.Rect.Top))
}

func (d *mirrorDisplay) Close() {}
