//go:build linux

package main

// Linux input injection via a /dev/uinput virtual pen tablet.
//
// A single virtual device ("Drawsplay Pen") is created advertising
// absolute X/Y, pressure, per-axis tilt, pen/eraser tools and stylus
// buttons. The kernel presents it as real input hardware; libinput and the
// compositor (Mutter/X11) handle it like a drawing tablet.
//
// Finger/touch input is injected on this same device using the
// BTN_TOOL_PEN tool code (not a separate touchscreen device, and not
// BTN_TOOL_FINGER). Two independent constraints forced this design:
//
//   - libinput's tablet-tool backend has no valid "finger" tool code (only
//     pen/rubber/brush/pencil/airbrush/mouse/lens) -- BTN_TOOL_FINGER on a
//     tablet-classified device is not handled as a proper contact, so taps
//     don't register as a click and a release can be dropped.
//   - Routing finger events through a separate uinput device explicitly
//     classified as a touchscreen (ID_INPUT_TOUCHSCREEN/INPUT_PROP_DIRECT)
//     fixes that libinput-level problem, but GNOME/Mutter's
//     org.gnome.desktop.peripherals.touchscreen "map to monitor" mechanism
//     does not reliably work for virtual/screencast outputs (unlike the
//     Wacom tablet mapping, which does) -- so a genuinely-separate
//     touchscreen device never gets routed to the Drawsplay virtual
//     monitor at all.
//
// Reporting finger contact as BTN_TOOL_PEN keeps it on the one device
// GNOME already maps correctly, and reuses the exact tablet-tool protocol
// that pen input already uses successfully (tip down/up = click, in/out of
// range handled by libinput's tablet-tool state machine, which does
// support proper tap and release semantics for pen-type contacts).
//
// Display mapping: absolute axes span 0..absRange and the compositor decides
// which monitor the tablet maps to. GNOME maps stylus devices through
// Settings → Wacom Tablet → Map to Monitor, keyed by the target monitor's
// connector vendor/product/serial; the host keeps this in sync automatically
// (see remapTabletInputToVirtualMonitor in display_linux.go) so touch/pen
// input follows the Drawsplay virtual monitor across reconnects without the
// user needing to redo it in Settings each session. The X11 extended display
// does the same through Muffin's/Mutter's tablet settings, and aims at the
// output's rectangle of the X screen itself where no compositor maps the
// device (see x11_tablet_linux.go and tabletAbsCoords).
//
// Requires write access to /dev/uinput: either run via sudo or add a udev
// rule granting the user access (see README).

import (
	"fmt"
	"log"
	"math"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Drawsplay Pen's uinput vendor/product IDs, also used as the dconf/gsettings
// lookup key for GNOME's per-tablet "Map to Monitor" setting (schema path
// /org/gnome/desktop/peripherals/tablets/<vendor>:<product>/) — see
// remapTabletInputToVirtualMonitor in display_linux.go.
const (
	drawsplayPenVendorID  = 0x1d50
	drawsplayPenProductID = 0x5044
)

// Linux input event constants (linux/input-event-codes.h).
const (
	evSyn = 0x00
	evKey = 0x01
	evAbs = 0x03

	synReport = 0

	btnToolPen    = 0x140
	btnToolRubber = 0x141
	btnTouch      = 0x14a
	btnStylus     = 0x14b
	btnStylus2    = 0x14c

	absX        = 0x00
	absY        = 0x01
	absPressure = 0x18
	absTiltX    = 0x1a
	absTiltY    = 0x1b

	// uinput ioctls (linux/uinput.h).
	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502
	uiDevSetup   = 0x405c5503 // _IOW('U', 3, struct uinput_setup)
	uiAbsSetup   = 0x401c5504 // _IOW('U', 4, struct uinput_abs_setup)
	uiSetEvBit   = 0x40045564 // _IOW('U', 100, int)
	uiSetKeyBit  = 0x40045565 // _IOW('U', 101, int)
	uiSetAbsBit  = 0x40045567 // _IOW('U', 103, int)

	absRange    = 32767
	pressureMax = 4096
	tiltRange   = 90 // degrees
)

// uinputSetup mirrors struct uinput_setup (input_id + name[80] + ff_max).
type uinputSetup struct {
	busType uint16
	vendor  uint16
	product uint16
	version uint16
	name    [80]byte
	ffMax   uint32
}

// uinputAbsSetup mirrors struct uinput_abs_setup.
type uinputAbsSetup struct {
	code    uint16
	_       uint16
	value   int32
	minimum int32
	maximum int32
	fuzz    int32
	flat    int32
	res     int32
}

// inputEvent mirrors struct input_event on 64-bit Linux.
type inputEvent struct {
	sec   int64
	usec  int64
	typ   uint16
	code  uint16
	value int32
}

// linuxInjector writes events to a uinput virtual pen tablet and a uinput
// virtual keyboard (M5 keyboard passthrough). Finger contact is reported
// via BTN_TOOL_PEN (see file header comment for why).
type linuxInjector struct {
	mu   sync.Mutex
	fd   int
	kbFd int // virtual keyboard fd, or -1 when unavailable
	gate penGate
	// Active tool key currently reported (btnToolPen/btnToolRubber), or 0
	// when out of range. Finger contact also uses btnToolPen.
	tool int
	// eventBuf/writeBuf are scratch buffers reused across inject/cancelAll/
	// injectKey calls to avoid a fresh heap allocation on every network
	// packet: pen/touch input can arrive at 100+ Hz, and each Android
	// "historical sample" batch element triggers one of these calls, so
	// per-call allocations translate directly into GC pressure on the
	// input-to-display latency path. Always accessed under mu, which every
	// caller already holds while writeEvents runs.
	eventBuf []inputEvent
	writeBuf []byte
}

// newInjector creates the uinput devices, degrading to a no-op (with a log
// message) when /dev/uinput is inaccessible.
func newInjector() injector {
	fd, err := unix.Open("/dev/uinput", unix.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		log.Printf("Input injection unavailable (/dev/uinput: %v); running capture-only. See README for udev setup.", err)
		setOperationalWarning("uinput-runtime", fmt.Sprintf("/dev/uinput is unavailable; input injection is disabled: %v", err))
		return noopInjector{}
	}
	if err := setupUinputDevice(fd); err != nil {
		_ = unix.Close(fd)
		log.Printf("Input injection unavailable (%v); running capture-only", err)
		setOperationalWarning("uinput-runtime", fmt.Sprintf("uinput setup failed; input injection is disabled: %v", err))
		return noopInjector{}
	}

	kbFd := -1
	if kfd, err := unix.Open("/dev/uinput", unix.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
		if err := setupUinputKeyboard(kfd); err != nil {
			_ = unix.Close(kfd)
			log.Printf("Keyboard passthrough unavailable (%v)", err)
		} else {
			kbFd = kfd
		}
	}
	setOperationalWarning("uinput-runtime", "")
	log.Printf("Input injection ready (uinput pen tablet, keyboard=%v)", kbFd >= 0)
	return &linuxInjector{fd: fd, kbFd: kbFd}
}

// setupUinputDevice declares capabilities and creates the device node.
func setupUinputDevice(fd int) error {
	set := func(req uintptr, val int) error {
		return unix.IoctlSetInt(fd, uint(req), val)
	}
	for _, ev := range []int{evKey, evAbs} {
		if err := set(uiSetEvBit, ev); err != nil {
			return fmt.Errorf("UI_SET_EVBIT: %w", err)
		}
	}
	for _, key := range []int{btnToolPen, btnToolRubber, btnTouch, btnStylus, btnStylus2} {
		if err := set(uiSetKeyBit, key); err != nil {
			return fmt.Errorf("UI_SET_KEYBIT: %w", err)
		}
	}
	absAxes := []uinputAbsSetup{
		{code: absX, maximum: absRange, res: 100},
		{code: absY, maximum: absRange, res: 100},
		{code: absPressure, maximum: pressureMax},
		{code: absTiltX, minimum: -tiltRange, maximum: tiltRange},
		{code: absTiltY, minimum: -tiltRange, maximum: tiltRange},
	}
	for i := range absAxes {
		if err := set(uiSetAbsBit, int(absAxes[i].code)); err != nil {
			return fmt.Errorf("UI_SET_ABSBIT: %w", err)
		}
		if err := ioctlPtr(fd, uiAbsSetup, unsafe.Pointer(&absAxes[i])); err != nil {
			return fmt.Errorf("UI_ABS_SETUP: %w", err)
		}
	}
	setup := uinputSetup{busType: 0x03 /* BUS_USB */, vendor: drawsplayPenVendorID, product: drawsplayPenProductID, version: 1}
	copy(setup.name[:], "Drawsplay Pen")
	if err := ioctlPtr(fd, uiDevSetup, unsafe.Pointer(&setup)); err != nil {
		return fmt.Errorf("UI_DEV_SETUP: %w", err)
	}
	if err := unix.IoctlSetInt(fd, uiDevCreate, 0); err != nil {
		return fmt.Errorf("UI_DEV_CREATE: %w", err)
	}
	return nil
}

// setupUinputKeyboard declares a full-keyboard device advertising every
// evdev code in the shared keymap (keymap.go) and creates the node.
func setupUinputKeyboard(fd int) error {
	if err := unix.IoctlSetInt(fd, uiSetEvBit, evKey); err != nil {
		return fmt.Errorf("UI_SET_EVBIT: %w", err)
	}
	for _, m := range androidKeycodes {
		if err := unix.IoctlSetInt(fd, uiSetKeyBit, int(m.evdev)); err != nil {
			return fmt.Errorf("UI_SET_KEYBIT(%d): %w", m.evdev, err)
		}
	}
	setup := uinputSetup{busType: 0x03 /* BUS_USB */, vendor: 0x1d50, product: 0x5045, version: 1}
	copy(setup.name[:], "Drawsplay Keyboard")
	if err := ioctlPtr(fd, uiDevSetup, unsafe.Pointer(&setup)); err != nil {
		return fmt.Errorf("UI_DEV_SETUP: %w", err)
	}
	if err := unix.IoctlSetInt(fd, uiDevCreate, 0); err != nil {
		return fmt.Errorf("UI_DEV_CREATE: %w", err)
	}
	return nil
}

func ioctlPtr(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func (l *linuxInjector) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = unix.IoctlSetInt(l.fd, uiDevDestroy, 0)
	_ = unix.Close(l.fd)
	if l.kbFd >= 0 {
		_ = unix.IoctlSetInt(l.kbFd, uiDevDestroy, 0)
		_ = unix.Close(l.kbFd)
		l.kbFd = -1
	}
}

func (l *linuxInjector) inject(msg *packet) {
	l.mu.Lock()
	defer l.mu.Unlock()

	pen := isPenTool(msg.ToolType)
	if pen {
		l.gate.notePen()
	} else if l.gate.blocksFinger() {
		return // palm rejection
	}

	// Finger contact is reported as btnToolPen (see file header comment):
	// libinput's tablet-tool backend has no valid finger tool code, and a
	// genuinely separate touchscreen uinput device doesn't get routed to
	// Mutter's virtual monitor, so finger reuses the pen's tool code to
	// get correct tap/drag/release handling on the one device GNOME maps
	// correctly.
	tool := btnToolPen
	if msg.ToolType == "eraser" {
		tool = btnToolRubber
	}

	events := l.eventBuf[:0]
	key := func(code int, value int32) {
		events = append(events, inputEvent{typ: evKey, code: uint16(code), value: value})
	}
	abs := func(code int, value int32) {
		events = append(events, inputEvent{typ: evAbs, code: uint16(code), value: value})
	}

	// Tool transitions (in/out of range, pen <-> eraser).
	inRange := msg.Action != "hover_exit" && msg.Action != "cancel" && msg.Action != "up" && msg.Action != "pointer_up"
	if l.tool != 0 && l.tool != tool {
		key(l.tool, 0)
	}
	if inRange && l.tool != tool {
		key(tool, 1)
	}

	// Position, pressure, tilt always accompany the event.
	if msg.Width > 0 && msg.Height > 0 {
		x, y := tabletAbsCoords(float64(msg.X)/float64(msg.Width), float64(msg.Y)/float64(msg.Height), activeTarget())
		abs(absX, x)
		abs(absY, y)
	}
	abs(absPressure, pressureValue(msg))
	tx, ty := tiltValues(msg)
	abs(absTiltX, tx)
	abs(absTiltY, ty)

	// Contact and button state.
	switch msg.Action {
	case "down", "pointer_down":
		key(btnTouch, 1)
	case "up", "pointer_up", "cancel":
		key(btnTouch, 0)
	}
	key(btnStylus, boolTo(msg.Buttons&32 != 0))  // BUTTON_STYLUS_PRIMARY
	key(btnStylus2, boolTo(msg.Buttons&64 != 0)) // BUTTON_STYLUS_SECONDARY

	if !inRange {
		if l.tool != 0 {
			key(l.tool, 0)
		}
		l.tool = 0
	} else {
		l.tool = tool
	}

	events = append(events, inputEvent{typ: evSyn, code: synReport})
	l.eventBuf = events
	l.writeEvents(l.fd, events)
}

// cancelAll lifts the contact and takes the current tool out of range, so a
// client that vanished mid-stroke doesn't leave the virtual pen pressed.
func (l *linuxInjector) cancelAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := append(l.eventBuf[:0],
		inputEvent{typ: evKey, code: uint16(btnTouch), value: 0},
		inputEvent{typ: evKey, code: uint16(btnStylus), value: 0},
		inputEvent{typ: evKey, code: uint16(btnStylus2), value: 0},
		inputEvent{typ: evAbs, code: uint16(absPressure), value: 0},
	)
	if l.tool != 0 {
		events = append(events, inputEvent{typ: evKey, code: uint16(l.tool), value: 0})
		l.tool = 0
	}
	events = append(events, inputEvent{typ: evSyn, code: synReport})
	l.eventBuf = events
	l.writeEvents(l.fd, events)
}

// injectKey forwards a tablet keyboard event through the virtual keyboard.
// Unmapped keycodes are dropped (no Unicode injection path exists on
// uinput; the keymap covers standard keyboards).
func (l *linuxInjector) injectKey(msg *packet) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.kbFd < 0 {
		return
	}
	var value int32
	switch msg.Action {
	case "down":
		value = 1
	case "up":
		value = 0
	default:
		return
	}
	m, ok := lookupKey(msg.Keycode)
	if !ok {
		return
	}
	events := append(l.eventBuf[:0],
		inputEvent{typ: evKey, code: m.evdev, value: value},
		inputEvent{typ: evSyn, code: synReport},
	)
	l.eventBuf = events
	l.writeEvents(l.kbFd, events)
}

func pressureValue(msg *packet) int32 {
	p := clampF(float64(msg.Pressure))
	inContact := msg.Action == "down" || msg.Action == "move" || msg.Action == "pointer_down"
	if inContact && p <= 0 {
		p = 1.0 / pressureMax
	}
	if !inContact && msg.Action != "up" && msg.Action != "pointer_up" {
		p = 0 // hovering
	}
	return int32(p * pressureMax)
}

// tiltValues decomposes Android's polar tilt (radians from vertical) and
// orientation (radians, 0 = up, clockwise) into per-axis degrees.
func tiltValues(msg *packet) (int32, int32) {
	deg := float64(msg.Tilt) * 180 / math.Pi
	o := float64(msg.Orientation)
	return int32(deg * math.Sin(o)), int32(-deg * math.Cos(o))
}

func clampF(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// tabletAbsCoords converts a normalized tablet position into absolute axis
// values. The axes normally span the display the device is mapped to. On an
// X11 extended display that nothing maps the device to (see
// x11_tablet_linux.go), the X server spreads them across the whole X screen,
// so the position is aimed at the display's rectangle within the screen; the
// tablet's edges land on the centers of the display's edge pixels, keeping
// input on the display.
func tabletAbsCoords(nx, ny float64, t *captureTarget) (int32, int32) {
	nx, ny = clampF(nx), clampF(ny)
	if t == nil || t.Kind != captureX11Grab || t.Rect.empty() || t.TabletMapped ||
		t.DesktopWidth <= 0 || t.DesktopHeight <= 0 {
		return int32(nx * absRange), int32(ny * absRange)
	}
	return x11ScreenAbs(t.Rect.X, t.Rect.W, t.DesktopWidth, nx),
		x11ScreenAbs(t.Rect.Y, t.Rect.H, t.DesktopHeight, ny)
}

// x11ScreenAbs returns the axis value the X server places at pixel
// offset+n*(size-1) of a screen axis of the given length: it scales a value v
// to v*length/(absRange+1).
func x11ScreenAbs(offset, size, length int, n float64) int32 {
	pixel := float64(offset) + n*float64(size-1) + 0.5
	v := math.Round(pixel * (absRange + 1) / float64(length))
	return int32(min(max(v, 0), absRange))
}

func boolTo(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// writeEvents writes the batch as raw input_event records. Reuses l.writeBuf
// across calls (caller holds l.mu) instead of allocating a fresh byte slice
// per pen/touch/key event, keeping this high-frequency path allocation-free
// once the buffer has grown to its steady-state size.
func (l *linuxInjector) writeEvents(fd int, events []inputEvent) {
	size := int(unsafe.Sizeof(inputEvent{}))
	buf := l.writeBuf[:0]
	for i := range events {
		b := (*[48]byte)(unsafe.Pointer(&events[i]))[:size:size]
		buf = append(buf, b...)
	}
	l.writeBuf = buf
	if _, err := unix.Write(fd, buf); err != nil {
		log.Printf("uinput write failed: %v", err)
	}
}
