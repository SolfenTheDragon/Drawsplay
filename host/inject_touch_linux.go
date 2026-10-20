//go:build linux

package main

// Multi-touch finger injection through a second uinput device,
// "Drawsplay Touch": a type-B multitouch touchscreen (INPUT_PROP_DIRECT), so
// the compositor sees real simultaneous contacts and can recognise
// two-finger scrolling, pinch and the three/four-finger shell gestures.
//
// The pen device (inject_linux.go) reports a finger as a single BTN_TOOL_PEN
// contact, which cannot carry gestures. This device is used for fingers only
// when the compositor's "touchscreen -> monitor" mapping for it was written for the
// session's virtual monitor (touchMapped), because an unmapped touchscreen
// would span the whole desktop and land touches on the wrong display. On an
// X11 output nothing maps, the coordinates are aimed at the output instead,
// as for the pen. Otherwise fingers keep using the pen device.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	drawsplayTouchVendorID  = 0x1d50
	drawsplayTouchProductID = 0x5046

	absMTSlot       = 0x2f
	absMTPositionX  = 0x35
	absMTPositionY  = 0x36
	absMTTrackingID = 0x39

	uiSetPropBit    = 0x4004556e // _IOW('U', 110, int)
	inputPropDirect = 0x01

	touchMaxSlots = 10
)

// touchMapped reports that the session's compositor maps the Drawsplay Touch
// device to the session's virtual monitor. It is cleared when the session's
// display target goes away (setActiveTarget).
var touchMapped atomic.Bool

// setTouchscreenOutput writes a compositor's "touchscreen -> monitor"
// setting for the Drawsplay Touch device. A variable for tests.
var setTouchscreenOutput = runGsettingsSetTouchscreenOutput

func runGsettingsSetTouchscreenOutput(c x11TabletCompositor, spec monitorSpec) error {
	if c.touchSchema == "" {
		return fmt.Errorf("%s has no touchscreen mapping", c.name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := fmt.Sprintf("%s%04x:%04x/", c.touchSchemaPath, drawsplayTouchVendorID, drawsplayTouchProductID)
	out, err := exec.CommandContext(ctx, "gsettings", "set", c.touchSchema+":"+path, "output",
		gvariantStringArray(spec.Vendor, spec.Product, spec.Serial)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gsettings set: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// noteTouchMapping records the outcome of mapping the Drawsplay Touch device
// onto output; without a mapping, fingers keep using the pen device.
func noteTouchMapping(label, output string, err error) {
	if err != nil {
		touchMapped.Store(false)
		log.Printf("%s: could not map Drawsplay Touch to %s (%v); fingers use the pen device (no multi-touch gestures)", label, output, err)
		return
	}
	touchMapped.Store(true)
	log.Printf("Mapped Drawsplay Touch multi-touch input to %s (%s)", output, label)
}

// mapGsettingsTouchscreen maps the touch device through c's touchscreen
// schema.
func mapGsettingsTouchscreen(c x11TabletCompositor, spec monitorSpec) {
	noteTouchMapping(c.name, spec.Connector, setTouchscreenOutput(c, spec))
}

func setupUinputTouch(fd int) error {
	if err := unix.IoctlSetInt(fd, uiSetEvBit, evKey); err != nil {
		return fmt.Errorf("UI_SET_EVBIT: %w", err)
	}
	if err := unix.IoctlSetInt(fd, uiSetEvBit, evAbs); err != nil {
		return fmt.Errorf("UI_SET_EVBIT: %w", err)
	}
	if err := unix.IoctlSetInt(fd, uiSetKeyBit, btnTouch); err != nil {
		return fmt.Errorf("UI_SET_KEYBIT: %w", err)
	}
	if err := unix.IoctlSetInt(fd, uiSetPropBit, inputPropDirect); err != nil {
		return fmt.Errorf("UI_SET_PROPBIT: %w", err)
	}
	axes := []uinputAbsSetup{
		{code: absX, maximum: absRange, res: 100},
		{code: absY, maximum: absRange, res: 100},
		{code: absMTSlot, maximum: touchMaxSlots - 1},
		{code: absMTTrackingID, maximum: 65535},
		{code: absMTPositionX, maximum: absRange, res: 100},
		{code: absMTPositionY, maximum: absRange, res: 100},
	}
	for i := range axes {
		if err := unix.IoctlSetInt(fd, uiSetAbsBit, int(axes[i].code)); err != nil {
			return fmt.Errorf("UI_SET_ABSBIT: %w", err)
		}
		if err := ioctlPtr(fd, uiAbsSetup, unsafe.Pointer(&axes[i])); err != nil {
			return fmt.Errorf("UI_ABS_SETUP: %w", err)
		}
	}
	setup := uinputSetup{busType: 0x03 /* BUS_USB */, vendor: drawsplayTouchVendorID, product: drawsplayTouchProductID, version: 1}
	copy(setup.name[:], "Drawsplay Touch")
	if err := ioctlPtr(fd, uiDevSetup, unsafe.Pointer(&setup)); err != nil {
		return fmt.Errorf("UI_DEV_SETUP: %w", err)
	}
	if err := unix.IoctlSetInt(fd, uiDevCreate, 0); err != nil {
		return fmt.Errorf("UI_DEV_CREATE: %w", err)
	}
	return nil
}

// touchState tracks the multitouch slots in use. Guarded by linuxInjector.mu.
type touchState struct {
	slots    [touchMaxSlots]int // pointer ID per slot, valid while used
	used     [touchMaxSlots]bool
	nextID   int32
	activeCt int
}

func (t *touchState) slotFor(pointerID int) (int, bool) {
	for i := range t.slots {
		if t.used[i] && t.slots[i] == pointerID {
			return i, true
		}
	}
	return 0, false
}

// useTouchDevice reports whether a finger packet goes to the multi-touch
// device: it exists and the session's display is one it addresses correctly.
// That is a display the compositor maps it to, or an X11 output that
// tabletAbsCoords aims at within the X screen.
func (l *linuxInjector) useTouchDevice() bool {
	if l.touchFd < 0 || globalSettingsStore == nil || !globalSettingsStore.get().TouchGestures {
		return false
	}
	t := activeTarget()
	if t == nil {
		return false
	}
	if t.TabletMapped {
		return touchMapped.Load()
	}
	return t.Kind == captureX11Grab
}

// injectTouch writes one finger packet to the multitouch device. Caller holds
// l.mu.
func (l *linuxInjector) injectTouch(msg *packet) {
	events := l.eventBuf[:0]
	ev := func(typ, code uint16, value int32) {
		events = append(events, inputEvent{typ: typ, code: code, value: value})
	}
	t := &l.touch

	if msg.Action == "cancel" {
		events = l.releaseAllTouches(events)
	} else {
		slot, ok := t.slotFor(msg.PointerID)
		switch msg.Action {
		case "down", "pointer_down", "move":
			if !ok {
				if msg.Action == "move" {
					break // a move without a contact is stale
				}
				for i := range t.used {
					if !t.used[i] {
						slot, ok = i, true
						t.used[i], t.slots[i] = true, msg.PointerID
						t.activeCt++
						t.nextID = (t.nextID + 1) & 0xffff
						ev(evAbs, absMTSlot, int32(slot))
						ev(evAbs, absMTTrackingID, t.nextID)
						if t.activeCt == 1 {
							ev(evKey, btnTouch, 1)
						}
						break
					}
				}
				if !ok {
					// Every slot busy means lifts were lost; recover instead of
					// ignoring touch until the app restarts.
					events = l.releaseAllTouches(events)
					break
				}
			}
			if msg.Width > 0 && msg.Height > 0 {
				x, y := tabletAbsCoords(float64(msg.X)/float64(msg.Width), float64(msg.Y)/float64(msg.Height), activeTarget())
				ev(evAbs, absMTSlot, int32(slot))
				ev(evAbs, absMTPositionX, x)
				ev(evAbs, absMTPositionY, y)
				ev(evAbs, absX, x)
				ev(evAbs, absY, y)
			}
		case "up", "pointer_up":
			if ok {
				ev(evAbs, absMTSlot, int32(slot))
				ev(evAbs, absMTTrackingID, -1)
				t.used[slot] = false
				t.activeCt--
				if t.activeCt == 0 {
					ev(evKey, btnTouch, 0)
				}
			}
		}
	}
	if len(events) == 0 {
		return
	}
	events = append(events, inputEvent{typ: evSyn, code: synReport})
	l.eventBuf = events
	l.writeEvents(l.touchFd, events)
}

// releaseAllTouches appends the events lifting every contact in use, one
// frame per contact: X11 clients (Qt/XI2) track a lone TouchEnd per frame more
// reliably than a simultaneous lift of every slot.
func (l *linuxInjector) releaseAllTouches(events []inputEvent) []inputEvent {
	t := &l.touch
	for i := range t.used {
		if !t.used[i] {
			continue
		}
		events = append(events,
			inputEvent{typ: evAbs, code: absMTSlot, value: int32(i)},
			inputEvent{typ: evAbs, code: absMTTrackingID, value: -1})
		t.used[i] = false
		t.activeCt--
		if t.activeCt == 0 {
			events = append(events, inputEvent{typ: evKey, code: uint16(btnTouch), value: 0})
		}
		events = append(events, inputEvent{typ: evSyn, code: synReport})
	}
	t.activeCt = 0
	return events
}

// cancelTouches lifts every contact on the multitouch device. Caller holds
// l.mu.
func (l *linuxInjector) cancelTouches() {
	if l.touchFd < 0 || l.touch.activeCt == 0 {
		return
	}
	events := l.releaseAllTouches(l.eventBuf[:0])
	events = append(events, inputEvent{typ: evSyn, code: synReport})
	l.eventBuf = events
	l.writeEvents(l.touchFd, events)
}

func clearTouchMapped() { touchMapped.Store(false) }
