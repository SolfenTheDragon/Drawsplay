package main

// Input injection abstraction (milestone M3).
//
// An injector turns tablet input packets into OS-level pointer events so the
// tablet acts as a real touchscreen/pen display for whatever it is showing:
//
//	inject_windows.go — synthetic pointer injection (InjectSyntheticPointerInput)
//	                    with full pen pressure/tilt/buttons and touch.
//	inject_linux.go   — /dev/uinput virtual pen tablet device.
//
// Coordinates arrive normalized to the tablet's view (x/y in view pixels,
// width/height the view size). Injectors map them onto the active video
// session's display rectangle (activeTarget in display.go) so drawing on the
// tablet lands on the display the tablet is showing.
//
// Palm rejection: while the pen is in range, finger touches are suppressed
// (penGate below). Resting a palm on the tablet while writing therefore does
// not produce stray touch input on the host.

import "time"

// injector consumes input packets and injects them into the OS.
// Implementations are called from a single goroutine per session; they must
// tolerate interleaved sessions (sessions are serialized per packet).
type injector interface {
	// inject processes one pointer input packet. Must never block on I/O
	// slower than the injection syscall itself (latency-sensitive path).
	inject(msg *packet)
	// injectKey processes one "key" packet from a physical keyboard
	// attached to the tablet (M5). Action is "down" or "up".
	injectKey(msg *packet)
	// cancelAll lifts any pointer state still held down (pen in contact,
	// touch contacts). Called when an input session ends so a client that
	// vanishes mid-stroke doesn't leave a button/contact stuck on the host.
	cancelAll()
	// close releases OS resources (virtual devices, handles).
	close()
}

// Each platform provides:
//
//	newInjector() injector
//
// which must return a working injector or a logging no-op — input capture
// diagnostics keep working even when injection is unavailable.

// noopInjector is used when platform injection is unavailable.
type noopInjector struct{}

func (noopInjector) inject(*packet)    {}
func (noopInjector) injectKey(*packet) {}
func (noopInjector) cancelAll()        {}
func (noopInjector) close()            {}

// penGate implements palm rejection: finger input is suppressed while the
// pen was recently in range or in contact.
type penGate struct {
	lastPen time.Time
}

const penGateHold = 500 * time.Millisecond

// notePen records pen activity (contact or hover).
func (g *penGate) notePen() { g.lastPen = time.Now() }

// blocksFinger reports whether finger events should currently be dropped.
func (g *penGate) blocksFinger() bool {
	return time.Since(g.lastPen) < penGateHold
}

// isPenTool reports whether the packet comes from an active stylus.
func isPenTool(toolType string) bool {
	return toolType == "stylus" || toolType == "eraser"
}

// isFingerRelease reports whether a finger action lifts a contact. Releases
// must reach the injector even during palm rejection (see penGate).
func isFingerRelease(action string) bool {
	return action == "up" || action == "pointer_up" || action == "cancel"
}
