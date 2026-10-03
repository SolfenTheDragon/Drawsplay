//go:build windows

package main

// Windows virtual display: Parsec VDD (extend mode) with a mirror fallback.
//
// Extend-mode flow:
//
//  1. Snapshot the current DXGI outputs (dxgi_windows.go).
//  2. Open the Parsec VDD control device and plug one display
//     (vdd_windows.go). Windows enumerates a new monitor within ~1 s.
//  3. Poll DXGI until an output appears that was not in the snapshot; its
//     (adapter, output) indices become the ddagrab capture target and its
//     desktop rectangle becomes the input-mapping rect.
//
// If the VDD driver is not installed, newVirtualDisplay logs a hint and
// degrades to mirroring the primary attached output, so video always works.

import (
	"fmt"
	"log"
	"strings"
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

// newVirtualDisplay creates the session display: a VDD virtual monitor when
// the driver is available, otherwise a mirror of the primary output.
//
// Plugging the VDD monitor connects it but Windows may leave it inactive
// (not part of the desktop) — observed on Windows 11 where the OS
// remembers monitor topology. If the new output has not appeared shortly
// after the ADD, the extend topology is re-applied via SetDisplayConfig,
// which activates every connected monitor (the same approach Parsec's own
// display manager uses).
func newVirtualDisplay(opts videoOptions) (virtualDisplay, error) {
	before, err := enumerateOutputs()
	if err != nil {
		return nil, fmt.Errorf("DXGI enumeration: %w", err)
	}

	dev, err := openOrRecoverVDDDevice()
	if err != nil {
		log.Printf("Parsec VDD unavailable (%v); falling back to mirroring the primary display. Install the driver for extend mode (see README).", err)
		return newMirrorDisplay(before)
	}

	knownOutputs := attachedOutputNames(before)
	index, out, err := plugVDDDisplay(dev, knownOutputs)
	if err != nil {
		log.Printf("Virtual display did not appear (%v); attempting VDD adapter self-recovery", err)
		newDev, newIndex, newOut, recoverErr := recoverVDDDisplaySession(dev, index, knownOutputs)
		if recoverErr != nil {
			dev.Close()
			log.Printf("VDD self-recovery failed (%v); falling back to mirror mode", recoverErr)
			return newMirrorDisplay(before)
		}
		dev, index, out = newDev, newIndex, newOut
	}

	changed := false
	if shouldRespectWindowsResolutionSetting() {
		ident := activeClientIdentity()
		if ident.NativeWidth > 0 && ident.NativeHeight > 0 {
			registered, reloadNeeded := registerVDDCustomResolution(ident.NativeWidth, ident.NativeHeight)
			if registered {
				log.Printf("Registered native resolution %dx%d for Windows display settings", ident.NativeWidth, ident.NativeHeight)
			}
			if reloadNeeded {
				log.Printf("Reloading VDD adapter to expose newly registered native mode %dx%d", ident.NativeWidth, ident.NativeHeight)
				newDev, newIndex, newOut, reloadErr := reloadVDDDisplay(dev, index, knownOutputs)
				if reloadErr != nil {
					log.Printf("VDD reload failed (%v); continuing with current display mode", reloadErr)
				} else {
					dev, index, out = newDev, newIndex, newOut
				}
			}
		}
		log.Printf("Windows + Android session: using Windows Display Settings as the resolution source of truth")
	} else {
		// Host settings are authoritative for non-Windows-Android sessions.
		modeW, modeH := opts.Width, opts.Height
		var reloadNeeded bool
		changed, reloadNeeded = applyTabletDisplayMode(out.DeviceName, modeW, modeH, opts.FPS)
		if reloadNeeded {
			// A new custom mode was registered; the VDD builds its mode list
			// when the adapter starts, so recycle the whole session display
			// (unplug, restart adapter, replug) and re-apply against the fresh
			// GDI device name.
			log.Printf("Reloading VDD adapter to expose newly registered mode %dx%d", modeW, modeH)
			newDev, newIndex, newOut, reloadErr := reloadVDDDisplay(dev, index, knownOutputs)
			if reloadErr != nil {
				log.Printf("VDD reload failed (%v); falling back to mirror mode", reloadErr)
				if outputs, enumErr := enumerateOutputs(); enumErr == nil {
					return newMirrorDisplay(outputs)
				}
				return newMirrorDisplay(before)
			}
			dev, index, out = newDev, newIndex, newOut
			changed, _ = applyTabletDisplayMode(out.DeviceName, modeW, modeH, opts.FPS)
		}
	}

	display := &vddDisplay{
		dev:          dev,
		index:        index,
		output:       out,
		knownOutputs: knownOutputs,
	}
	if changed {
		if relocated, relocateErr := relocateOutput(out, knownOutputs); relocateErr == nil {
			display.output = relocated
		} else {
			log.Printf("Virtual display relocation still settling: %v", relocateErr)
		}
	}

	return display, nil
}

func openOrRecoverVDDDevice() (*vddDevice, error) {
	dev, err := openVDDDevice()
	if err == nil {
		return dev, nil
	}
	log.Printf("Opening VDD control device failed (%v); attempting adapter restart", err)
	if restartErr := restartVDDAdapterAuto(); restartErr != nil {
		return nil, fmt.Errorf("open VDD device: %w (and adapter restart failed: %v)", err, restartErr)
	}
	dev, reopenErr := reopenVDDDevice(15 * time.Second)
	if reopenErr != nil {
		return nil, fmt.Errorf("open VDD device after adapter restart: %w", reopenErr)
	}
	log.Printf("VDD control device recovered after adapter restart")
	return dev, nil
}

func shouldRespectWindowsResolutionSetting() bool {
	ident := activeClientIdentity()
	return strings.EqualFold(strings.TrimSpace(ident.Platform), "android")
}

// plugVDDDisplay adds one virtual display and waits for its DXGI output to
// become attached, forcing the extend topology if Windows leaves the new
// monitor inactive. knownOutputs is the set of GDI device names attached
// before this VDD session; the new output is the one not in it.
func plugVDDDisplay(dev *vddDevice, knownOutputs map[string]bool) (int, displayOutput, error) {
	index, err := dev.AddDisplay()
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
		dev.RemoveDisplay(index)
		return -1, displayOutput{}, err
	}
	return index, out, nil
}

// reloadVDDDisplay makes a newly registered custom resolution available to a
// live session. The VDD reads HKLM\SOFTWARE\Parsec\vdd\0 when the adapter
// starts, so the sequence is: unplug the virtual monitor, close the control
// handle, restart the adapter, reopen the device and plug a fresh display.
// Restarting the adapter underneath an open control handle or a plugged
// monitor is never safe — it destroys the monitor, invalidates the handle
// and churns the GDI device name out from under the mode-setting code.
func reloadVDDDisplay(dev *vddDevice, index int, knownOutputs map[string]bool) (*vddDevice, int, displayOutput, error) {
	dev.RemoveDisplay(index)
	waitForVDDOutputGone(knownOutputs, 3*time.Second)
	dev.Close()

	if err := restartVDDAdapterAuto(); err != nil {
		// Best-effort: replug anyway so the session recovers on the old
		// mode list instead of dying.
		log.Printf("VDD adapter restart failed (%v); replugging without mode-list reload", err)
	}

	newDev, err := reopenVDDDevice(10 * time.Second)
	if err != nil {
		return nil, -1, displayOutput{}, fmt.Errorf("reopen VDD control device: %w", err)
	}
	newIndex, out, err := plugVDDDisplay(newDev, knownOutputs)
	if err != nil {
		newDev.Close()
		return nil, -1, displayOutput{}, fmt.Errorf("replug virtual display after adapter reload: %w", err)
	}
	return newDev, newIndex, out, nil
}

func recoverVDDDisplaySession(dev *vddDevice, index int, knownOutputs map[string]bool) (*vddDevice, int, displayOutput, error) {
	if index >= 0 {
		dev.RemoveDisplay(index)
		waitForVDDOutputGone(knownOutputs, 3*time.Second)
	}
	dev.Close()
	if err := restartVDDAdapterAuto(); err != nil {
		log.Printf("VDD adapter restart during session recovery failed: %v", err)
	}
	newDev, err := reopenVDDDevice(15 * time.Second)
	if err != nil {
		return nil, -1, displayOutput{}, fmt.Errorf("reopen VDD control device: %w", err)
	}
	newIndex, out, err := plugVDDDisplay(newDev, knownOutputs)
	if err != nil {
		newDev.Close()
		return nil, -1, displayOutput{}, fmt.Errorf("replug virtual display after VDD recovery: %w", err)
	}
	return newDev, newIndex, out, nil
}

// reopenVDDDevice retries opening the VDD control device while the adapter
// finishes re-enumerating after a restart.
func reopenVDDDevice(timeout time.Duration) (*vddDevice, error) {
	deadline := time.Now().Add(timeout)
	for {
		dev, err := openVDDDevice()
		if err == nil {
			return dev, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// relocateOutput re-enumerates outputs to pick up the display's new desktop
// rect and output index after a mode change. Applying a mode can cause
// Windows to assign the live VDD monitor a new GDI display name.
func relocateOutput(out displayOutput, knownOutputs map[string]bool) (displayOutput, error) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if outputs, err := enumerateOutputs(); err == nil {
			if resolved, err := resolveVDDOutput(out, knownOutputs, outputs); err == nil {
				return resolved, nil
			}
		}
		time.Sleep(vddOutputPollInterval)
	}
	return displayOutput{}, fmt.Errorf("display %s could not be re-resolved after mode change", out.DeviceName)
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
		Kind:       captureDDAGrab,
		Adapter:    o.AdapterIndex,
		Output:     o.OutputIndex,
		DeviceName: o.DeviceName,
		Width:      int(r.Right - r.Left),
		Height:     int(r.Bottom - r.Top),
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
// the single attached output that was not present before AddDisplay. This
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

// vddDisplay is a live Parsec VDD virtual monitor.
type vddDisplay struct {
	dev          *vddDevice
	index        int
	output       displayOutput
	knownOutputs map[string]bool
}

func (d *vddDisplay) Target() captureTarget { return targetFor(d.output) }

// Refresh re-locates the virtual monitor by GDI name or, after a name churn,
// by the output added for this VDD session.
func (d *vddDisplay) Refresh() (captureTarget, error) {
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
	return targetFor(d.output), nil
}

func (d *vddDisplay) Description() string {
	return fmt.Sprintf("virtual display %s (Parsec VDD #%d, adapter %d output %d, %dx%d)",
		d.output.DeviceName, d.index, d.output.AdapterIndex, d.output.OutputIndex,
		int(d.output.Rect.Right-d.output.Rect.Left), int(d.output.Rect.Bottom-d.output.Rect.Top))
}

func (d *vddDisplay) Recover() error {
	newDev, newIndex, newOut, err := recoverVDDDisplaySession(d.dev, d.index, d.knownOutputs)
	if err != nil {
		return err
	}
	d.dev, d.index, d.output = newDev, newIndex, newOut
	log.Printf("Recovered Parsec VDD session display as %s", d.output.DeviceName)
	return nil
}

// Close removes the virtual display and waits briefly for Windows to
// actually detach the monitor. The next session's newVirtualDisplay diffs a
// "before" snapshot against post-AddDisplay enumeration; returning while the
// old monitor is still attached (or its GDI device name still in flux) makes
// that diff misidentify the new output. Waiting here keeps handoffs
// deterministic: one display out, then one display in.
func (d *vddDisplay) Close() {
	d.dev.RemoveDisplay(d.index)
	waitForVDDOutputGone(d.knownOutputs, 3*time.Second)
	d.dev.Close()
}

// waitForVDDOutputGone polls until every attached output is from the
// pre-session baseline. This remains correct if Windows renamed the VDD
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
// VDD driver is unavailable.
type mirrorDisplay struct {
	output displayOutput
}

// newMirrorDisplay picks the primary attached output — the one whose desktop
// rectangle starts at (0,0) — or the first attached output as a last resort.
func newMirrorDisplay(outputs []displayOutput) (virtualDisplay, error) {
	var chosen *displayOutput
	for i := range outputs {
		o := &outputs[i]
		if !o.Attached {
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

func (d *mirrorDisplay) Target() captureTarget { return targetFor(d.output) }

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
	return targetFor(d.output), nil
}

func (d *mirrorDisplay) Description() string {
	return fmt.Sprintf("mirror of %s (adapter %d output %d, %dx%d)",
		d.output.DeviceName, d.output.AdapterIndex, d.output.OutputIndex,
		int(d.output.Rect.Right-d.output.Rect.Left), int(d.output.Rect.Bottom-d.output.Rect.Top))
}

func (d *mirrorDisplay) Close() {}
