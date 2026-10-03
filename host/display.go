package main

// Virtual display abstraction (milestone M2).
//
// A virtualDisplay represents one OS-level virtual monitor created for the
// lifetime of a video session: it is created when a tablet's video channel
// connects and destroyed when it disconnects, so the desktop only extends
// while the tablet is actually attached.
//
// Per-OS implementations (each also provides a mirror fallback so video
// still works when the virtual-display mechanism is unavailable):
//
//	display_windows.go — Parsec VDD driver (IddCx) controlled via IOCTLs.
//	display_linux.go   — GNOME Mutter ScreenCast RecordVirtual (Wayland),
//	                     x11grab mirror fallback for X11 sessions.
//
// The captureTarget returned by a display tells the platform pipeline
// builder (pipeline_windows.go / pipeline_linux.go) what to record.

import (
	"sync"
	"sync/atomic"
)

// captureKind selects the capture mechanism a pipeline builder must use.
type captureKind string

const (
	// captureDDAGrab: Windows DXGI Desktop Duplication of one output.
	captureDDAGrab captureKind = "ddagrab"
	// capturePipeWire: Linux PipeWire stream node (Mutter screen cast).
	capturePipeWire captureKind = "pipewire"
	// captureX11Grab: Linux X11 root capture (mirror fallback).
	captureX11Grab captureKind = "x11grab"
)

// captureTarget is the union of per-OS capture parameters.
type captureTarget struct {
	Kind captureKind

	// DXGI adapter and output indices (Kind == captureDDAGrab).
	Adapter int
	Output  int

	// PipeWire node ID (Kind == capturePipeWire).
	Node uint32

	// Requested stream dimensions (used where the mechanism can negotiate
	// them, e.g. Mutter virtual monitors).
	Width  int
	Height int

	// Desktop-space rectangle of the display (virtual desktop coordinates
	// on Windows; logical layout coordinates on Linux). Input injection
	// (M3) maps normalized tablet coordinates into this rectangle. Zero
	// Rect means "unknown" — injectors then target the primary display.
	Rect displayRect

	// GDI device name of the display (Windows, e.g. `\\.\DISPLAY5`).
	// Lets the injector re-resolve Rect live when the display mode or
	// position changes mid-session. Empty when unknown.
	DeviceName string
}

// displayRect is a display's position and size in desktop coordinates.
type displayRect struct {
	X, Y, W, H int
}

func (r displayRect) empty() bool { return r.W <= 0 || r.H <= 0 }

// currentTarget publishes the active video session's capture target so the
// input channel can map tablet coordinates onto the right display. nil means
// no video session is active (injectors fall back to the primary display).
var currentTarget atomic.Pointer[captureTarget]

func setActiveTarget(t *captureTarget) { currentTarget.Store(t) }
func activeTarget() *captureTarget     { return currentTarget.Load() }

// Mode-change fan-out: when the session display's resolution changes
// mid-session (the user adjusted it in the OS display settings), the video
// loop publishes the new mode and each input session pushes it to its
// tablet, which persists it as the new preference.
var (
	modeSubsMu    sync.Mutex
	modeSubs      = map[uint64]func(w, h int){}
	clearModeSubs = map[uint64]func(){}
)

func subscribeModeChanges(id uint64, fn func(w, h int)) {
	modeSubsMu.Lock()
	defer modeSubsMu.Unlock()
	modeSubs[id] = fn
}

func unsubscribeModeChanges(id uint64) {
	modeSubsMu.Lock()
	defer modeSubsMu.Unlock()
	delete(modeSubs, id)
}

func publishModeChange(w, h int) {
	modeSubsMu.Lock()
	fns := make([]func(w, h int), 0, len(modeSubs))
	for _, fn := range modeSubs {
		fns = append(fns, fn)
	}
	modeSubsMu.Unlock()
	for _, fn := range fns {
		fn(w, h)
	}
}

func subscribeModePreferenceClears(id uint64, fn func()) {
	modeSubsMu.Lock()
	defer modeSubsMu.Unlock()
	clearModeSubs[id] = fn
}

func unsubscribeModePreferenceClears(id uint64) {
	modeSubsMu.Lock()
	defer modeSubsMu.Unlock()
	delete(clearModeSubs, id)
}

func publishModePreferenceClear() bool {
	modeSubsMu.Lock()
	fns := make([]func(), 0, len(clearModeSubs))
	for _, fn := range clearModeSubs {
		fns = append(fns, fn)
	}
	modeSubsMu.Unlock()
	for _, fn := range fns {
		fn()
	}
	return len(fns) > 0
}

// virtualDisplay is one live virtual (or fallback mirror) display.
type virtualDisplay interface {
	// Target describes what the capture pipeline should record.
	Target() captureTarget

	// Refresh re-resolves the display against the current OS topology and
	// returns the updated capture target. Moving or rearranging monitors
	// (e.g. in Windows display settings) can change a display's desktop
	// rectangle and DXGI output index while invalidating in-flight capture.
	// An error means the target is temporarily unresolved; callers must not
	// start capture with the previous, potentially stale output index.
	Refresh() (captureTarget, error)

	// Description is a human-readable summary for logs.
	Description() string

	// Close removes the virtual display and releases all resources.
	Close()
}

// Each platform provides:
//
//	newVirtualDisplay(opts videoOptions) (virtualDisplay, error)
//	buildCapturePipelines(target captureTarget, opts videoOptions) ([]pipelineSpec, error)
//
// newVirtualDisplay must degrade to a mirror of the primary display (with a
// log message) when the virtual-display mechanism is unavailable, so video
// always works; it returns an error only when no capture is possible at all.
