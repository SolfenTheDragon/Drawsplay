//! Linux input injection through /dev/uinput: a virtual keyboard, a multitouch screen and a
//! pen tablet. Needs write access to /dev/uinput (see `packaging/99-drawsplay-desktop.rules`).
//!
//! The touch and pen devices are absolute devices that span the whole desktop, so positions are
//! scaled against the full desktop size (see [`to_axis`]); this is exact on X11. Wayland
//! compositors may map absolute devices differently.

use super::scancodes::linux_key_hid;
use super::Target;
use dd_proto::{PointerPhase, PointerSource};
use evdev::uinput::VirtualDevice;
use evdev::{AbsInfo, AttributeSet, BusType, InputEvent, InputId, KeyCode, PropType, UinputAbsSetup};
use evdev::{AbsoluteAxisCode, RelativeAxisCode};
use std::collections::HashMap;
use std::io;

const EV_SYN: u16 = 0x00;
const EV_KEY: u16 = 0x01;
const EV_ABS: u16 = 0x03;
const SYN_REPORT: u16 = 0;

const ABS_X: u16 = 0x00;
const ABS_Y: u16 = 0x01;
const ABS_PRESSURE: u16 = 0x18;
const ABS_TILT_X: u16 = 0x1a;
const ABS_TILT_Y: u16 = 0x1b;
/// Tilt resolution in units per radian, so 1 unit is 1 degree (180/pi = 57.3). libinput reads a
/// tilt axis without a resolution as Wacom's physical +/-64 degrees, which squeezed our +/-90
/// degree range to 71% (a 45 degree lean reached apps as 32 degrees).
const TILT_UNITS_PER_RADIAN: i32 = 57;
const ABS_MT_SLOT: u16 = 0x2f;
const ABS_MT_POSITION_X: u16 = 0x35;
const ABS_MT_POSITION_Y: u16 = 0x36;
const ABS_MT_TRACKING_ID: u16 = 0x39;
const ABS_MT_PRESSURE: u16 = 0x3a;

const BTN_LEFT: u16 = 0x110;
const BTN_RIGHT: u16 = 0x111;
const BTN_MIDDLE: u16 = 0x112;
const EV_REL: u16 = 0x02;
const REL_HWHEEL: u16 = 0x06;
const REL_WHEEL: u16 = 0x08;

const BTN_TOOL_PEN: u16 = 0x140;
const BTN_TOOL_RUBBER: u16 = 0x141;
const BTN_TOUCH: u16 = 0x14a;
const BTN_STYLUS: u16 = 0x14b;
const BTN_STYLUS2: u16 = 0x14c;

/// Range of the absolute X/Y axes.
pub const AXIS_MAX: i32 = 32767;
const PRESSURE_MAX: i32 = 4095;
const SLOTS: i32 = 10;

/// Map desktop pixel (`ax`, `ay`) onto 0..=AXIS_MAX across the whole desktop.
pub fn to_axis(ax: i32, ay: i32, t: Target) -> (i32, i32) {
    let (dw, dh) = if t.desktop == (0, 0) {
        // Unknown desktop size: assume the captured area reaches the bottom-right corner.
        ((t.origin.0.max(0) as u32) + t.size.0, (t.origin.1.max(0) as u32) + t.size.1)
    } else {
        t.desktop
    };
    let scale = |v: i32, len: u32| -> i32 {
        let span = (len.saturating_sub(1)).max(1) as i64;
        ((v.max(0) as i64 * AXIS_MAX as i64) / span).clamp(0, AXIS_MAX as i64) as i32
    };
    (scale(ax, dw), scale(ay, dh))
}

pub fn target_for_x11(mut target: Target, query: &crate::linux_display::Query, output: Option<&str>) -> Target {
    if query.screen.0 > 0 && query.screen.1 > 0 {
        target.desktop = query.screen;
    }
    if let Some((_, _, x, y)) = query.outputs.iter().find(|o| Some(o.name.as_str()) == output).and_then(|o| o.geometry)
    {
        target.origin = (x, y);
    }
    target
}

pub fn target_for_wayland(mut target: Target, outputs: &[crate::wayland_display::Out], output: Option<&str>) -> Target {
    let (x, y, w, h) = crate::wayland_display::bounds(outputs);
    if w > 0 && h > 0 {
        target.desktop = (w, h);
        if let Some(out) = outputs.iter().find(|o| Some(o.name.as_str()) == output) {
            target.origin = (out.x - x, out.y - y);
        }
    }
    target
}

fn abs(code: u16, min: i32, max: i32, res: i32) -> UinputAbsSetup {
    UinputAbsSetup::new(AbsoluteAxisCode(code), AbsInfo::new(0, min, max, 0, 0, res))
}

fn ev(kind: u16, code: u16, value: i32) -> InputEvent {
    InputEvent::new(kind, code, value)
}

pub struct Uinput {
    keyboard: VirtualDevice,
    touch: VirtualDevice,
    pen: VirtualDevice,
    /// Absolute pointer, only on Wayland (X11 moves the cursor through enigo/XTest, Wayland cannot).
    mouse: Option<VirtualDevice>,
    mouse_buttons: u8,
    /// client contact id -> multitouch slot
    slots: HashMap<u8, i32>,
    next_tracking: i32,
    pen_down: bool,
    pen_in_range: bool,
}

impl Uinput {
    pub fn new() -> io::Result<Uinput> {
        let id = || InputId::new(BusType::BUS_VIRTUAL, 0x1d6b, 0xd5d1, 1);

        let mut keys = AttributeSet::<KeyCode>::new();
        for k in (1u16..=127).chain(183..=194) {
            keys.insert(KeyCode::new(k));
        }
        let keyboard =
            VirtualDevice::builder()?.name("DrawSplay Desktop Keyboard").input_id(id()).with_keys(&keys)?.build()?;

        let mut direct = AttributeSet::<PropType>::new();
        direct.insert(PropType::DIRECT);

        let mut tkeys = AttributeSet::<KeyCode>::new();
        tkeys.insert(KeyCode::new(BTN_TOUCH));
        let touch = VirtualDevice::builder()?
            .name("DrawSplay Desktop Touch")
            .input_id(id())
            .with_properties(&direct)?
            .with_keys(&tkeys)?
            .with_absolute_axis(&abs(ABS_X, 0, AXIS_MAX, 100))?
            .with_absolute_axis(&abs(ABS_Y, 0, AXIS_MAX, 100))?
            .with_absolute_axis(&abs(ABS_MT_SLOT, 0, SLOTS - 1, 0))?
            .with_absolute_axis(&abs(ABS_MT_TRACKING_ID, 0, 65535, 0))?
            .with_absolute_axis(&abs(ABS_MT_POSITION_X, 0, AXIS_MAX, 100))?
            .with_absolute_axis(&abs(ABS_MT_POSITION_Y, 0, AXIS_MAX, 100))?
            .with_absolute_axis(&abs(ABS_MT_PRESSURE, 0, PRESSURE_MAX, 0))?
            .build()?;

        let mut pkeys = AttributeSet::<KeyCode>::new();
        for k in [BTN_TOOL_PEN, BTN_TOOL_RUBBER, BTN_TOUCH, BTN_STYLUS, BTN_STYLUS2] {
            pkeys.insert(KeyCode::new(k));
        }
        let pen = VirtualDevice::builder()?
            .name("DrawSplay Desktop Pen")
            .input_id(id())
            .with_properties(&direct)?
            .with_keys(&pkeys)?
            .with_absolute_axis(&abs(ABS_X, 0, AXIS_MAX, 100))?
            .with_absolute_axis(&abs(ABS_Y, 0, AXIS_MAX, 100))?
            .with_absolute_axis(&abs(ABS_PRESSURE, 0, PRESSURE_MAX, 0))?
            .with_absolute_axis(&abs(ABS_TILT_X, -90, 90, TILT_UNITS_PER_RADIAN))?
            .with_absolute_axis(&abs(ABS_TILT_Y, -90, 90, TILT_UNITS_PER_RADIAN))?
            .build()?;

        let mouse = if std::env::var_os("WAYLAND_DISPLAY").is_some() {
            let mut mkeys = AttributeSet::<KeyCode>::new();
            for k in [BTN_LEFT, BTN_RIGHT, BTN_MIDDLE] {
                mkeys.insert(KeyCode::new(k));
            }
            let mut rel = AttributeSet::<RelativeAxisCode>::new();
            rel.insert(RelativeAxisCode(REL_WHEEL));
            rel.insert(RelativeAxisCode(REL_HWHEEL));
            Some(
                VirtualDevice::builder()?
                    .name("DrawSplay Desktop Mouse")
                    .input_id(id())
                    .with_keys(&mkeys)?
                    .with_relative_axes(&rel)?
                    .with_absolute_axis(&abs(ABS_X, 0, AXIS_MAX, 100))?
                    .with_absolute_axis(&abs(ABS_Y, 0, AXIS_MAX, 100))?
                    .build()?,
            )
        } else {
            None
        };

        // The desktop notices a new input device a moment after it appears; events sent before that
        // are lost (the first key or stroke of a session would vanish).
        std::thread::sleep(std::time::Duration::from_millis(400));

        Ok(Uinput {
            keyboard,
            touch,
            pen,
            mouse,
            mouse_buttons: 0,
            slots: HashMap::new(),
            next_tracking: 1,
            pen_down: false,
            pen_in_range: false,
        })
    }

    /// Returns false if the key has no Linux mapping.
    pub fn key(&mut self, hid: u16, down: bool) -> bool {
        let Some(code) = linux_key_hid(hid) else { return false };
        let evs = [ev(EV_KEY, code, down as i32), ev(EV_SYN, SYN_REPORT, 0)];
        self.keyboard.emit(&evs).is_ok()
    }

    #[allow(clippy::too_many_arguments)]
    pub fn pointer(
        &mut self,
        id: u8,
        source: PointerSource,
        phase: PointerPhase,
        x: i32,
        y: i32,
        pressure: f32,
        buttons: u8,
        tilt_x: i8,
        tilt_y: i8,
    ) -> bool {
        let p = (pressure.clamp(0.0, 1.0) * PRESSURE_MAX as f32).round() as i32;
        match source {
            PointerSource::Pen => self.pen_event(phase, x, y, p, buttons, tilt_x, tilt_y),
            PointerSource::Touch => self.touch_event(id, phase, x, y, p.max(PRESSURE_MAX / 10)),
            PointerSource::Mouse => false,
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn pen_event(&mut self, phase: PointerPhase, x: i32, y: i32, p: i32, buttons: u8, tx: i8, ty: i8) -> bool {
        let mut evs = Vec::with_capacity(10);
        let tool = if buttons & 16 != 0 { BTN_TOOL_RUBBER } else { BTN_TOOL_PEN };
        match phase {
            PointerPhase::Down | PointerPhase::Move | PointerPhase::Hover => {
                if !self.pen_in_range {
                    evs.push(ev(EV_KEY, tool, 1));
                    self.pen_in_range = true;
                }
                evs.push(ev(EV_ABS, ABS_X, x));
                evs.push(ev(EV_ABS, ABS_Y, y));
                evs.push(ev(EV_ABS, ABS_TILT_X, tx as i32));
                evs.push(ev(EV_ABS, ABS_TILT_Y, ty as i32));
                evs.push(ev(EV_ABS, ABS_PRESSURE, if phase == PointerPhase::Hover { 0 } else { p }));
                evs.push(ev(EV_KEY, BTN_STYLUS, (buttons & 2 != 0) as i32));
                evs.push(ev(EV_KEY, BTN_STYLUS2, (buttons & 8 != 0) as i32));
                let touching = phase != PointerPhase::Hover;
                if touching != self.pen_down {
                    evs.push(ev(EV_KEY, BTN_TOUCH, touching as i32));
                    self.pen_down = touching;
                }
            }
            PointerPhase::Up => {
                evs.push(ev(EV_ABS, ABS_PRESSURE, 0));
                if self.pen_down {
                    evs.push(ev(EV_KEY, BTN_TOUCH, 0));
                    self.pen_down = false;
                }
                if self.pen_in_range {
                    evs.push(ev(EV_KEY, BTN_TOOL_PEN, 0));
                    evs.push(ev(EV_KEY, BTN_TOOL_RUBBER, 0));
                    self.pen_in_range = false;
                }
            }
            PointerPhase::Wheel => return false,
        }
        evs.push(ev(EV_SYN, SYN_REPORT, 0));
        self.pen.emit(&evs).is_ok()
    }

    fn touch_event(&mut self, id: u8, phase: PointerPhase, x: i32, y: i32, p: i32) -> bool {
        let mut evs = Vec::with_capacity(8);
        match phase {
            PointerPhase::Down => {
                let slot = match self.slots.get(&id) {
                    Some(s) => *s,
                    None => {
                        let Some(free) = (0..SLOTS).find(|s| !self.slots.values().any(|u| u == s)) else {
                            return false;
                        };
                        self.slots.insert(id, free);
                        free
                    }
                };
                let first = self.slots.len() == 1;
                evs.push(ev(EV_ABS, ABS_MT_SLOT, slot));
                evs.push(ev(EV_ABS, ABS_MT_TRACKING_ID, self.next_tracking));
                self.next_tracking = (self.next_tracking + 1) % 65535;
                evs.push(ev(EV_ABS, ABS_MT_POSITION_X, x));
                evs.push(ev(EV_ABS, ABS_MT_POSITION_Y, y));
                evs.push(ev(EV_ABS, ABS_MT_PRESSURE, p));
                if first {
                    evs.push(ev(EV_KEY, BTN_TOUCH, 1));
                    evs.push(ev(EV_ABS, ABS_X, x));
                    evs.push(ev(EV_ABS, ABS_Y, y));
                }
            }
            PointerPhase::Move => {
                let Some(&slot) = self.slots.get(&id) else { return false };
                evs.push(ev(EV_ABS, ABS_MT_SLOT, slot));
                evs.push(ev(EV_ABS, ABS_MT_POSITION_X, x));
                evs.push(ev(EV_ABS, ABS_MT_POSITION_Y, y));
                evs.push(ev(EV_ABS, ABS_MT_PRESSURE, p));
            }
            PointerPhase::Up => {
                let Some(slot) = self.slots.remove(&id) else { return false };
                evs.push(ev(EV_ABS, ABS_MT_SLOT, slot));
                evs.push(ev(EV_ABS, ABS_MT_TRACKING_ID, -1));
                if self.slots.is_empty() {
                    evs.push(ev(EV_KEY, BTN_TOUCH, 0));
                }
            }
            _ => return false,
        }
        evs.push(ev(EV_SYN, SYN_REPORT, 0));
        self.touch.emit(&evs).is_ok()
    }

    pub fn has_mouse(&self) -> bool {
        self.mouse.is_some()
    }

    /// Absolute pointer move (axis units), button mask change (1 left, 2 right, 4 middle) and
    /// whole wheel steps (positive = up / right), in one report.
    pub fn mouse_event(&mut self, pos: Option<(i32, i32)>, buttons: Option<u8>, wheel: (i32, i32)) -> bool {
        let Some(dev) = self.mouse.as_mut() else { return false };
        let mut evs = Vec::with_capacity(8);
        if let Some((x, y)) = pos {
            evs.push(ev(EV_ABS, ABS_X, x));
            evs.push(ev(EV_ABS, ABS_Y, y));
        }
        if let Some(new) = buttons {
            for (bit, code) in [(1u8, BTN_LEFT), (2, BTN_RIGHT), (4, BTN_MIDDLE)] {
                let (was, is) = (self.mouse_buttons & bit != 0, new & bit != 0);
                if was != is {
                    evs.push(ev(EV_KEY, code, is as i32));
                }
            }
            self.mouse_buttons = new;
        }
        if wheel.0 != 0 {
            evs.push(ev(EV_REL, REL_HWHEEL, wheel.0));
        }
        if wheel.1 != 0 {
            evs.push(ev(EV_REL, REL_WHEEL, wheel.1));
        }
        if evs.is_empty() {
            return true;
        }
        evs.push(ev(EV_SYN, SYN_REPORT, 0));
        dev.emit(&evs).is_ok()
    }

    pub fn release_all(&mut self) {
        if self.mouse_buttons != 0 {
            self.mouse_event(None, Some(0), (0, 0));
        }
        let ids: Vec<u8> = self.slots.keys().copied().collect();
        for id in ids {
            self.touch_event(id, PointerPhase::Up, 0, 0, 0);
        }
        if self.pen_in_range || self.pen_down {
            self.pen_event(PointerPhase::Up, 0, 0, 0, 0, 0, 0);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tilt_units_are_degrees_for_libinput() {
        // libinput: degrees = value / resolution (radians) converted to degrees.
        for deg in [1, 30, 45, 60, 90] {
            let seen = (deg as f64 / TILT_UNITS_PER_RADIAN as f64).to_degrees();
            assert!((seen - deg as f64).abs() < 0.6, "{deg} degrees arrive as {seen}");
        }
    }

    fn t(origin: (i32, i32), size: (u32, u32), desktop: (u32, u32)) -> Target {
        Target { origin, size, desktop }
    }

    #[test]
    fn maps_a_secondary_monitor_into_the_whole_desktop() {
        // 1920x1080 primary, 1280x800 virtual monitor to its right: desktop is 3200x1080.
        let tg = t((1920, 0), (1280, 800), (3200, 1080));
        let (x0, y0) = to_axis(1920, 0, tg);
        let (x1, _) = to_axis(3199, 799, tg);
        assert_eq!(y0, 0);
        assert_eq!(x1, AXIS_MAX); // the right edge of the monitor is the right edge of the desktop
        assert_eq!(x0, (1920i64 * AXIS_MAX as i64 / 3199) as i32);
    }

    #[test]
    fn unknown_desktop_assumes_capture_reaches_the_corner() {
        let tg = t((0, 0), (1280, 720), (0, 0));
        assert_eq!(to_axis(1279, 719, tg), (AXIS_MAX, AXIS_MAX));
        assert_eq!(to_axis(0, 0, tg), (0, 0));
    }

    #[test]
    fn out_of_range_is_clamped() {
        let tg = t((0, 0), (100, 100), (100, 100));
        assert_eq!(to_axis(-50, 9999, tg), (0, AXIS_MAX));
    }

    #[test]
    fn existing_x11_contact_tracks_desktop_growth_and_shrinkage() {
        let original = t((1920, 0), (1280, 800), (3200, 1080));
        let query = |width| crate::linux_display::Query { outputs: Vec::new(), screen: (width, 1080) };
        let grown = target_for_x11(original, &query(4480), None);
        assert_eq!(to_axis(3199, 799, grown).0, (3199i64 * AXIS_MAX as i64 / 4479) as i32);
        let shrunk = target_for_x11(grown, &query(3200), None);
        assert_eq!(to_axis(3199, 799, shrunk).0, AXIS_MAX);
        let moved = crate::linux_display::parse_query(
            "Screen 0: current 3200 x 1080, maximum 16384 x 16384\nVIRTUAL1 connected 1280x800+0+0",
        );
        assert_eq!(target_for_x11(original, &moved, Some("VIRTUAL1")).origin, (0, 0));
    }

    #[test]
    fn wayland_contact_tracks_output_layout_origin_changes() {
        let original = t((1920, 0), (1280, 800), (3200, 1080));
        let outputs = crate::wayland_display::parse_sway(
            r#"[{"name":"real","active":true,"rect":{"x":-1920,"y":0,"width":1920,"height":1080}},
                {"name":"HEADLESS-1","active":true,"rect":{"x":0,"y":0,"width":1280,"height":800}},
                {"name":"HEADLESS-2","active":true,"rect":{"x":1280,"y":0,"width":1280,"height":800}}]"#,
        );
        let expanded = target_for_wayland(original, &outputs, Some("HEADLESS-1"));
        assert_eq!(expanded.desktop, (4480, 1080));
        assert_eq!(expanded.origin, (1920, 0));
        let shrunk = target_for_wayland(expanded, &outputs[1..], Some("HEADLESS-1"));
        assert_eq!(shrunk.desktop, (2560, 800));
        assert_eq!(shrunk.origin, (0, 0));
        assert_eq!(to_axis(1279, 799, shrunk).1, AXIS_MAX);
    }
}
