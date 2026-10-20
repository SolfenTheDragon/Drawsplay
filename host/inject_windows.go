//go:build windows

package main

// Windows input injection via synthetic pointer devices (Win10 1809+).
//
// Two synthetic devices are created (user32 CreateSyntheticPointerDevice):
// a PT_PEN device carrying full stylus state (pressure, tilt, barrel/eraser
// buttons, hover) and a PT_TOUCH device for finger input (up to
// maxTouchContacts simultaneous contacts). Packets are translated into
// InjectSyntheticPointerInput calls; Windows then routes them through the
// normal pointer stack, so apps see them as real pen/touch hardware
// (including pressure-sensitive ink in drawing apps).
//
// Coordinate mapping: normalized tablet coordinates → the active video
// session's display rectangle (extend mode: the virtual monitor; mirror
// mode: the mirrored output), falling back to the primary display when no
// video session is running.
//
// Struct layouts (offsets) follow winuser.h for 64-bit builds; the raw
// byte-slice encoding avoids fragile Go struct padding assumptions.

import (
	"log"
	"math"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Pointer input constants (winuser.h).
const (
	ptTouch = 2
	ptPen   = 3

	pointerFeedbackNone = 3

	pointerFlagNone      = 0x00000000
	pointerFlagInRange   = 0x00000002
	pointerFlagInContact = 0x00000004
	pointerFlagPrimary   = 0x00002000
	pointerFlagCanceled  = 0x00008000
	pointerFlagDown      = 0x00010000
	pointerFlagUpdate    = 0x00020000
	pointerFlagUp        = 0x00040000

	penFlagBarrel   = 0x00000001
	penFlagInverted = 0x00000002
	penFlagEraser   = 0x00000004

	penMaskPressure = 0x00000001
	penMaskTiltX    = 0x00000004
	penMaskTiltY    = 0x00000008

	touchMaskPressure = 0x00000004

	maxTouchContacts = 10

	// Windows pen pressure range for injection.
	penPressureMax = 1024
	// Android stylus button bits (MotionEvent.BUTTON_STYLUS_*).
	androidButtonStylusPrimary   = 32
	androidButtonStylusSecondary = 64

	// POINTER_TYPE_INFO layout: 4-byte type + 4 padding + union.
	ptiSize        = 152
	ptiUnionOffset = 8
	// POINTER_INFO field offsets within the union.
	piPointerType    = 0
	piPointerID      = 4
	piPointerFlags   = 12
	piPixelLocationX = 32
	piPixelLocationY = 36
	piSize           = 96
	// POINTER_PEN_INFO fields (after the embedded POINTER_INFO).
	penFlagsOff    = piSize + 0
	penMaskOff     = piSize + 4
	penPressureOff = piSize + 8
	penTiltXOff    = piSize + 16
	penTiltYOff    = piSize + 20
	// POINTER_TOUCH_INFO fields (after the embedded POINTER_INFO).
	touchFlagsOff    = piSize + 0
	touchMaskOff     = piSize + 4
	touchPressureOff = piSize + 44
)

var (
	user32                           = windows.NewLazySystemDLL("user32.dll")
	procCreateSyntheticPointerDevice = user32.NewProc("CreateSyntheticPointerDevice")
	procDestroySyntheticPointerDev   = user32.NewProc("DestroySyntheticPointerDevice")
	procInjectSyntheticPointerInput  = user32.NewProc("InjectSyntheticPointerInput")
	procGetSystemMetrics             = user32.NewProc("GetSystemMetrics")
	procEnumDisplaySettingsExW       = user32.NewProc("EnumDisplaySettingsExW")
	procSendInput                    = user32.NewProc("SendInput")
)

// System metrics for the primary display fallback rect and the virtual
// screen (bounding box of all monitors).
const (
	smCxScreen       = 0
	smCyScreen       = 1
	smXVirtualScreen = 76
	smYVirtualScreen = 77
)

// winInjector injects pen and touch input through synthetic pointer devices.
type winInjector struct {
	mu       sync.Mutex
	penDev   uintptr
	touchDev uintptr
	gate     penGate
	penInUse bool  // pen currently in contact (for cancelAll)
	penX     int32 // last injected pen position
	penY     int32
	contacts map[int]*touchContact // active finger contacts by Android pointer ID

	// Live display-rect cache (see mapToScreen): the session display's
	// rect is re-resolved by GDI device name at most every rectCacheTTL,
	// so injection tracks mid-session resolution/position changes even
	// while the video pipeline is still restarting.
	rectName string
	rectVal  displayRect
	rectAt   time.Time
}

const rectCacheTTL = 500 * time.Millisecond

// touchContact is the retained state of one finger, re-sent with every touch
// frame (Windows expects the full contact set per injection).
type touchContact struct {
	x, y     int32
	pressure uint32
	down     bool
}

// newInjector creates the synthetic devices, degrading to a no-op (with a
// log message) if the API is unavailable (pre-1809 Windows).
func newInjector() injector {
	if err := procCreateSyntheticPointerDevice.Find(); err != nil {
		log.Printf("Input injection unavailable (%v); running capture-only", err)
		return noopInjector{}
	}
	pen, _, _ := procCreateSyntheticPointerDevice.Call(ptPen, 1, pointerFeedbackNone)
	touch, _, _ := procCreateSyntheticPointerDevice.Call(ptTouch, maxTouchContacts, pointerFeedbackNone)
	if pen == 0 && touch == 0 {
		log.Printf("CreateSyntheticPointerDevice failed; input injection disabled")
		return noopInjector{}
	}
	log.Printf("Input injection ready (pen=%v touch=%v)", pen != 0, touch != 0)
	return &winInjector{penDev: pen, touchDev: touch, contacts: make(map[int]*touchContact)}
}

func (w *winInjector) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.penDev != 0 {
		_, _, _ = procDestroySyntheticPointerDev.Call(w.penDev)
	}
	if w.touchDev != 0 {
		_, _, _ = procDestroySyntheticPointerDev.Call(w.touchDev)
	}
	w.penDev, w.touchDev = 0, 0
}

func (w *winInjector) inject(msg *packet) {
	w.mu.Lock()
	defer w.mu.Unlock()

	x, y := w.mapToScreen(msg)
	if isPenTool(msg.ToolType) {
		w.gate.notePen()
		if w.penDev != 0 {
			w.injectPen(msg, x, y)
		}
		return
	}
	// Palm rejection: drop finger input while the pen is in range.
	if w.gate.blocksFinger() && !isFingerRelease(msg.Action) {
		return
	}
	if w.touchDev != 0 {
		w.injectTouch(msg, x, y)
	}
}

// mapToScreen converts normalized tablet coordinates to desktop pixels on
// the active session's display (or the primary display when idle).
func (w *winInjector) mapToScreen(msg *packet) (int32, int32) {
	rect := displayRect{}
	if t := activeTarget(); t != nil {
		rect = t.Rect
		if t.DeviceName != "" {
			if live := w.liveRect(t.DeviceName); !live.empty() {
				rect = live
			}
		}
	}
	if rect.empty() {
		cx, _, _ := procGetSystemMetrics.Call(smCxScreen)
		cy, _, _ := procGetSystemMetrics.Call(smCyScreen)
		rect = displayRect{X: 0, Y: 0, W: int(cx), H: int(cy)}
	}
	fx, fy := float64(0), float64(0)
	if msg.Width > 0 && msg.Height > 0 {
		fx = float64(msg.X) / float64(msg.Width)
		fy = float64(msg.Y) / float64(msg.Height)
	}
	fx = clamp01(fx)
	fy = clamp01(fy)
	x := int32(float64(rect.X) + fx*float64(rect.W-1))
	y := int32(float64(rect.Y) + fy*float64(rect.H-1))
	// InjectSyntheticPointerInput expects coordinates relative to the
	// virtual-screen origin, not raw desktop coordinates: with a monitor
	// left of / above the primary the origin is negative and unadjusted
	// coordinates clamp to the desktop edge.
	vx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	vy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	return x - int32(vx), y - int32(vy)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// liveRect returns the current desktop rect of the named GDI display,
// re-queried at most every rectCacheTTL (caller holds w.mu). A resolution
// or position change in display settings is reflected here within the TTL,
// keeping injection mapped correctly while the capture pipeline is still
// catching up. Returns a zero rect when the display cannot be resolved
// (e.g. mid-modeset); callers fall back to the session's last-known rect.
func (w *winInjector) liveRect(deviceName string) displayRect {
	now := time.Now()
	if w.rectName == deviceName && now.Sub(w.rectAt) < rectCacheTTL {
		return w.rectVal
	}
	w.rectName, w.rectAt, w.rectVal = deviceName, now, displayRect{}

	// DEVMODEW offsets: dmSize=68, dmPosition=76/80, dmPelsWidth=172,
	// dmPelsHeight=176 (winuser.h, 64-bit layout, no driver extra).
	const devmodeSize = 220
	var dm [devmodeSize]byte
	putU32(dm[:], 68, devmodeSize) // dmSize (u16, high half is dmDriverExtra=0)
	name, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return displayRect{}
	}
	const enumCurrentSettings = ^uintptr(0) // ENUM_CURRENT_SETTINGS (-1)
	ret, _, _ := procEnumDisplaySettingsExW.Call(
		uintptr(unsafe.Pointer(name)), enumCurrentSettings,
		uintptr(unsafe.Pointer(&dm[0])), 0)
	if ret == 0 {
		return displayRect{}
	}
	w.rectVal = displayRect{
		X: int(int32(getU32(dm[:], 76))),
		Y: int(int32(getU32(dm[:], 80))),
		W: int(int32(getU32(dm[:], 172))),
		H: int(int32(getU32(dm[:], 176))),
	}
	return w.rectVal
}

// pointerFlagsFor maps a packet action to POINTER_FLAG_* values. ok=false
// means the action does not translate to an injection (e.g. "other").
func pointerFlagsFor(action string, hoverCapable bool) (uint32, bool) {
	switch action {
	case "down", "pointer_down":
		return pointerFlagDown | pointerFlagInRange | pointerFlagInContact, true
	case "move":
		return pointerFlagUpdate | pointerFlagInRange | pointerFlagInContact, true
	case "up", "pointer_up":
		return pointerFlagUp | pointerFlagInRange, true
	case "cancel":
		return pointerFlagUp | pointerFlagCanceled, true
	case "hover_enter", "hover_move", "button_press", "button_release":
		if !hoverCapable {
			return 0, false
		}
		return pointerFlagUpdate | pointerFlagInRange, true
	case "hover_exit":
		if !hoverCapable {
			return 0, false
		}
		return pointerFlagUpdate, true
	default:
		return 0, false
	}
}

// injectPen sends one POINTER_TYPE_INFO frame on the pen device.
func (w *winInjector) injectPen(msg *packet, x, y int32) {
	flags, ok := pointerFlagsFor(msg.Action, true)
	if !ok {
		return
	}
	flags |= pointerFlagPrimary

	var penFlags uint32
	if msg.Buttons&androidButtonStylusPrimary != 0 {
		penFlags |= penFlagBarrel
	}
	if msg.ToolType == "eraser" {
		penFlags |= penFlagInverted | penFlagEraser
	}

	var buf [ptiSize]byte
	putU32(buf[:], 0, ptPen) // POINTER_TYPE_INFO.type
	u := buf[ptiUnionOffset:]
	putU32(u, piPointerType, ptPen)
	putU32(u, piPointerID, 0)
	putU32(u, piPointerFlags, flags)
	putI32(u, piPixelLocationX, x)
	putI32(u, piPixelLocationY, y)
	putU32(u, penFlagsOff, penFlags)
	putU32(u, penMaskOff, penMaskPressure|penMaskTiltX|penMaskTiltY)
	putU32(u, penPressureOff, penPressure(msg.Pressure))
	tx, ty := penTilt(msg.Tilt, msg.Orientation)
	putI32(u, penTiltXOff, tx)
	putI32(u, penTiltYOff, ty)

	ret, _, err := procInjectSyntheticPointerInput.Call(w.penDev, uintptr(unsafe.Pointer(&buf[0])), 1)
	if ret == 0 {
		log.Printf("Pen injection failed: %v", err)
	}
	w.penInUse = flags&pointerFlagInContact != 0
	w.penX, w.penY = x, y
}

// penPressure converts Android's 0..1 (nominal) pressure to Windows 0..1024.
func penPressure(p float32) uint32 {
	if p <= 0 {
		return 1 // in contact implies nonzero pressure
	}
	if p > 1 {
		p = 1
	}
	return uint32(p * penPressureMax)
}

// penTilt decomposes Android's polar tilt (radians from vertical) and
// orientation (radians, 0 = up, clockwise) into Windows per-axis degrees.
func penTilt(tilt, orientation float32) (int32, int32) {
	deg := float64(tilt) * 180 / math.Pi
	return int32(deg * math.Sin(float64(orientation))), int32(-deg * math.Cos(float64(orientation)))
}

// injectTouch updates the contact set from the packet and re-injects the
// full frame (Windows requires all active contacts in every call).
func (w *winInjector) injectTouch(msg *packet, x, y int32) {
	flags, ok := pointerFlagsFor(msg.Action, false)
	if !ok {
		return
	}

	id := msg.PointerID
	c := w.contacts[id]
	if c == nil {
		c = &touchContact{}
		w.contacts[id] = c
	}
	c.x, c.y = x, y
	c.pressure = uint32(clamp01(float64(msg.Pressure)) * penPressureMax)
	c.down = flags&pointerFlagInContact != 0

	// Build the frame: the packet's contact uses this action's flags; other
	// active contacts are re-sent as in-contact updates.
	frame := make([]byte, 0, ptiSize*len(w.contacts))
	count := 0
	for cid, contact := range w.contacts {
		var cflags uint32
		if cid == id {
			cflags = flags
		} else {
			cflags = pointerFlagUpdate | pointerFlagInRange | pointerFlagInContact
		}
		var buf [ptiSize]byte
		putU32(buf[:], 0, ptTouch)
		u := buf[ptiUnionOffset:]
		putU32(u, piPointerType, ptTouch)
		putU32(u, piPointerID, uint32(cid))
		putU32(u, piPointerFlags, cflags)
		putI32(u, piPixelLocationX, contact.x)
		putI32(u, piPixelLocationY, contact.y)
		putU32(u, touchFlagsOff, 0)
		putU32(u, touchMaskOff, touchMaskPressure)
		putU32(u, touchPressureOff, contact.pressure)
		frame = append(frame, buf[:]...)
		count++
		if count == maxTouchContacts {
			break
		}
	}
	// Contacts that just lifted leave the set after being injected once.
	if !c.down {
		delete(w.contacts, id)
	}
	if count == 0 {
		return
	}
	ret, _, err := procInjectSyntheticPointerInput.Call(w.touchDev, uintptr(unsafe.Pointer(&frame[0])), uintptr(count))
	if ret == 0 {
		log.Printf("Touch injection failed: %v", err)
	}
}

// cancelAll lifts any pointer state left behind by a vanished client: a pen
// still in contact and any active touch contacts would otherwise stay stuck
// (e.g. a drag that never ends) until the next session.
func (w *winInjector) cancelAll() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.penInUse && w.penDev != 0 {
		var buf [ptiSize]byte
		putU32(buf[:], 0, ptPen)
		u := buf[ptiUnionOffset:]
		putU32(u, piPointerType, ptPen)
		putU32(u, piPointerID, 0)
		putU32(u, piPointerFlags, pointerFlagUp|pointerFlagCanceled|pointerFlagPrimary)
		putI32(u, piPixelLocationX, w.penX)
		putI32(u, piPixelLocationY, w.penY)
		_, _, _ = procInjectSyntheticPointerInput.Call(w.penDev, uintptr(unsafe.Pointer(&buf[0])), 1)
	}
	w.penInUse = false

	if len(w.contacts) > 0 && w.touchDev != 0 {
		frame := make([]byte, 0, ptiSize*len(w.contacts))
		count := 0
		for cid, contact := range w.contacts {
			var buf [ptiSize]byte
			putU32(buf[:], 0, ptTouch)
			u := buf[ptiUnionOffset:]
			putU32(u, piPointerType, ptTouch)
			putU32(u, piPointerID, uint32(cid))
			putU32(u, piPointerFlags, pointerFlagUp|pointerFlagCanceled)
			putI32(u, piPixelLocationX, contact.x)
			putI32(u, piPixelLocationY, contact.y)
			frame = append(frame, buf[:]...)
			count++
			if count == maxTouchContacts {
				break
			}
		}
		_, _, _ = procInjectSyntheticPointerInput.Call(w.touchDev, uintptr(unsafe.Pointer(&frame[0])), uintptr(count))
	}
	w.contacts = make(map[int]*touchContact)
}

func putU32(b []byte, off int, v uint32) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
	b[off+2] = byte(v >> 16)
	b[off+3] = byte(v >> 24)
}

func putI32(b []byte, off int, v int32) { putU32(b, off, uint32(v)) }

func getU32(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

// --- Keyboard passthrough (M5) ---
//
// Physical keyboards attached to the tablet inject through SendInput.
// Mapped keys use virtual-key codes so the host keyboard layout and
// shortcuts (Ctrl+C, Alt+Tab, …) work naturally; unmapped keys that carry
// text fall back to per-character KEYEVENTF_UNICODE injection on key-down.

// INPUT struct layout for SendInput (64-bit): 4 type + 4 pad + KEYBDINPUT
// (2 wVk, 2 wScan, 4 dwFlags, 4 time, 4 pad, 8 dwExtraInfo) + 8 union pad.
const (
	inputKeyboard = 1
	inputSize     = 40

	keyeventfKeyUp   = 0x0002
	keyeventfUnicode = 0x0004
)

// keyboardInput encodes one INPUT{type=INPUT_KEYBOARD} record.
func keyboardInput(vk, scan uint16, flags uint32) [inputSize]byte {
	var buf [inputSize]byte
	putU32(buf[:], 0, inputKeyboard)
	buf[8] = byte(vk)
	buf[9] = byte(vk >> 8)
	buf[10] = byte(scan)
	buf[11] = byte(scan >> 8)
	putU32(buf[:], 12, flags)
	return buf
}

func (w *winInjector) injectKey(msg *packet) {
	down := msg.Action == "down"
	if !down && msg.Action != "up" {
		return
	}

	if m, ok := lookupKey(msg.Keycode); ok {
		var flags uint32
		if !down {
			flags = keyeventfKeyUp
		}
		buf := keyboardInput(m.vk, 0, flags)
		w.sendInputs(buf[:], 1)
		return
	}

	// Unicode fallback: inject the produced characters on key-down only
	// (down+up per char; a held unmapped key repeats via repeated downs).
	if !down || msg.Chars == "" {
		return
	}
	var frame []byte
	count := 0
	for _, r := range msg.Chars {
		for _, cu := range utf16Units(r) {
			d := keyboardInput(0, cu, keyeventfUnicode)
			u := keyboardInput(0, cu, keyeventfUnicode|keyeventfKeyUp)
			frame = append(frame, d[:]...)
			frame = append(frame, u[:]...)
			count += 2
		}
	}
	if count > 0 {
		w.sendInputs(frame, count)
	}
}

// utf16Units returns the UTF-16 code units for one rune (1 or 2 units).
func utf16Units(r rune) []uint16 {
	if r < 0x10000 {
		return []uint16{uint16(r)}
	}
	r -= 0x10000
	return []uint16{0xD800 + uint16(r>>10), 0xDC00 + uint16(r&0x3FF)}
}

func (w *winInjector) sendInputs(frame []byte, count int) {
	ret, _, err := procSendInput.Call(
		uintptr(count),
		uintptr(unsafe.Pointer(&frame[0])),
		inputSize,
	)
	if int(ret) != count {
		log.Printf("SendInput injected %d/%d events: %v", ret, count, err)
	}
}
