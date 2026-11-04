//! Windows input injection: scancode keyboard via `SendInput`, and synthetic pen and
//! touch devices (Windows Ink pointer injection) so pressure, tilt and multi-touch survive.

use super::scancodes::scan;
use dd_proto::PointerPhase;
use std::collections::HashMap;
use std::mem::size_of;
use windows::Win32::Foundation::{POINT, RECT};
use windows::Win32::UI::Controls::{
    CreateSyntheticPointerDevice, DestroySyntheticPointerDevice, HSYNTHETICPOINTERDEVICE, POINTER_FEEDBACK_DEFAULT,
    POINTER_TYPE_INFO, POINTER_TYPE_INFO_0,
};
use windows::Win32::UI::Input::KeyboardAndMouse::{
    SendInput, INPUT, INPUT_0, INPUT_KEYBOARD, KEYBDINPUT, KEYBD_EVENT_FLAGS, KEYEVENTF_EXTENDEDKEY, KEYEVENTF_KEYUP,
    KEYEVENTF_SCANCODE, VIRTUAL_KEY,
};
use windows::Win32::UI::Input::Pointer::{
    InjectSyntheticPointerInput, POINTER_FLAGS, POINTER_FLAG_DOWN, POINTER_FLAG_FIRSTBUTTON, POINTER_FLAG_INCONTACT,
    POINTER_FLAG_INRANGE, POINTER_FLAG_NEW, POINTER_FLAG_SECONDBUTTON, POINTER_FLAG_UP, POINTER_FLAG_UPDATE,
    POINTER_INFO, POINTER_PEN_INFO, POINTER_TOUCH_INFO,
};
use windows::Win32::UI::WindowsAndMessaging::{
    PEN_FLAG_BARREL, PEN_FLAG_ERASER, PEN_MASK_PRESSURE, PEN_MASK_TILT_X, PEN_MASK_TILT_Y, PT_PEN, PT_TOUCH,
    TOUCH_FLAG_NONE, TOUCH_MASK_CONTACTAREA, TOUCH_MASK_ORIENTATION, TOUCH_MASK_PRESSURE,
};

/// Press or release a physical key by HID usage. Returns false if the key has no mapping.
pub fn key(hid: u16, down: bool) -> bool {
    let Some(sc) = scan(hid) else { return false };
    let mut flags = KEYEVENTF_SCANCODE;
    if sc.ext {
        flags |= KEYEVENTF_EXTENDEDKEY;
    }
    if !down {
        flags |= KEYEVENTF_KEYUP;
    }
    let input = INPUT {
        r#type: INPUT_KEYBOARD,
        Anonymous: INPUT_0 {
            ki: KEYBDINPUT {
                wVk: VIRTUAL_KEY(0),
                wScan: sc.code as u16,
                dwFlags: KEYBD_EVENT_FLAGS(flags.0),
                time: 0,
                dwExtraInfo: 0,
            },
        },
    };
    unsafe { SendInput(&[input], size_of::<INPUT>() as i32) == 1 }
}

/// Move the cursor to a physical pixel of the virtual desktop. (enigo's absolute move only
/// spans the primary monitor, so it can never reach a monitor placed beside or above it.)
pub fn move_cursor(x: i32, y: i32) -> bool {
    use windows::Win32::UI::WindowsAndMessaging::SetCursorPos;
    unsafe { SetCursorPos(x, y).is_ok() }
}

#[derive(Clone, Copy)]
struct Contact {
    x: i32,
    y: i32,
    pressure: u32,
}

/// Synthetic pen and touch devices.
pub struct Pointers {
    pen: Option<HSYNTHETICPOINTERDEVICE>,
    touch: Option<HSYNTHETICPOINTERDEVICE>,
    contacts: HashMap<u8, Contact>,
    pen_seen: bool,
    /// Last pen position while the tip is down, so a vanished client can lift it.
    pen_down: Option<(i32, i32)>,
    pen_in_range: bool,
}

impl Drop for Pointers {
    fn drop(&mut self) {
        unsafe {
            if let Some(d) = self.pen.take() {
                DestroySyntheticPointerDevice(d);
            }
            if let Some(d) = self.touch.take() {
                DestroySyntheticPointerDevice(d);
            }
        }
    }
}

/// Inject a frame; log the first failure only (a stuck device would otherwise spam).
fn inject(dev: HSYNTHETICPOINTERDEVICE, frame: &[POINTER_TYPE_INFO], what: &str) -> bool {
    use std::sync::atomic::{AtomicBool, Ordering};
    static LOGGED: AtomicBool = AtomicBool::new(false);
    match unsafe { InjectSyntheticPointerInput(dev, frame) } {
        Ok(()) => true,
        Err(e) => {
            if !LOGGED.swap(true, Ordering::Relaxed) {
                eprintln!("{what} injection failed: {e}");
            }
            false
        }
    }
}

/// Top-left of the virtual desktop (negative when a monitor sits left of or above the primary).
/// Injected pointer coordinates are relative to it, not to the primary monitor.
fn virtual_origin() -> (i32, i32) {
    use windows::Win32::UI::WindowsAndMessaging::{GetSystemMetrics, SM_XVIRTUALSCREEN, SM_YVIRTUALSCREEN};
    unsafe { (GetSystemMetrics(SM_XVIRTUALSCREEN), GetSystemMetrics(SM_YVIRTUALSCREEN)) }
}

fn pressure_1024(p: f32) -> u32 {
    (p.clamp(0.0, 1.0) * 1024.0).round() as u32
}

impl Pointers {
    /// `None` when Windows refuses both devices (older builds, or a locked-down session).
    pub fn new() -> Option<Pointers> {
        let pen = unsafe { CreateSyntheticPointerDevice(PT_PEN, 1, POINTER_FEEDBACK_DEFAULT) }.ok();
        let touch = unsafe { CreateSyntheticPointerDevice(PT_TOUCH, 10, POINTER_FEEDBACK_DEFAULT) }.ok();
        if pen.is_none() && touch.is_none() {
            return None;
        }
        let mut p =
            Pointers { pen, touch, contacts: HashMap::new(), pen_seen: false, pen_down: None, pen_in_range: false };
        p.warm_up();
        Some(p)
    }

    /// Windows drops the first few samples of a fresh synthetic pen device; spend them on
    /// out-of-range updates now so the user's first stroke is not clipped.
    fn warm_up(&mut self) {
        if self.pen.is_some() {
            for _ in 0..8 {
                self.pen_sample(PointerPhase::Wheel, 0, 0, 0.0, 0, 0, 0);
            }
        }
    }

    pub fn has_pen(&self) -> bool {
        self.pen.is_some()
    }
    pub fn has_touch(&self) -> bool {
        self.touch.is_some()
    }

    /// Inject one pen sample at physical screen pixel (`x`, `y`).
    #[allow(clippy::too_many_arguments)]
    pub fn pen(
        &mut self,
        phase: PointerPhase,
        x: i32,
        y: i32,
        pressure: f32,
        buttons: u8,
        tilt_x: i8,
        tilt_y: i8,
    ) -> bool {
        // Windows only honours a pen that has been seen hovering in range first.
        if matches!(phase, PointerPhase::Down | PointerPhase::Move) && !self.pen_in_range {
            self.pen_sample(PointerPhase::Hover, x, y, 0.0, buttons, tilt_x, tilt_y);
        }
        let ok = self.pen_sample(phase, x, y, pressure, buttons, tilt_x, tilt_y);
        if phase == PointerPhase::Up {
            // Lift out of range as well, so the next stroke starts with a fresh hover.
            self.pen_sample(PointerPhase::Wheel, x, y, 0.0, 0, 0, 0);
        }
        ok
    }

    /// One raw pen sample. `Wheel` is used internally to mean "leave range".
    #[allow(clippy::too_many_arguments)]
    fn pen_sample(
        &mut self,
        phase: PointerPhase,
        x: i32,
        y: i32,
        pressure: f32,
        buttons: u8,
        tilt_x: i8,
        tilt_y: i8,
    ) -> bool {
        let Some(dev) = self.pen else { return false };
        let (vx, vy) = virtual_origin();
        let (x, y) = (x - vx, y - vy);
        let mut flags = POINTER_FLAGS(0);
        match phase {
            PointerPhase::Down => {
                flags |= POINTER_FLAG_DOWN | POINTER_FLAG_INRANGE | POINTER_FLAG_INCONTACT | POINTER_FLAG_FIRSTBUTTON;
            }
            PointerPhase::Move => {
                flags |= POINTER_FLAG_UPDATE | POINTER_FLAG_INRANGE | POINTER_FLAG_INCONTACT | POINTER_FLAG_FIRSTBUTTON;
            }
            PointerPhase::Hover => flags |= POINTER_FLAG_UPDATE | POINTER_FLAG_INRANGE,
            PointerPhase::Up => flags |= POINTER_FLAG_UP,
            PointerPhase::Wheel => flags |= POINTER_FLAG_UPDATE, // out of range
        }
        if !self.pen_seen {
            flags |= POINTER_FLAG_NEW;
            self.pen_seen = true;
        }
        if buttons & 2 != 0 {
            flags |= POINTER_FLAG_SECONDBUTTON;
        }
        let mut info = POINTER_PEN_INFO {
            pointerInfo: POINTER_INFO {
                pointerType: PT_PEN,
                pointerId: 1,
                pointerFlags: flags,
                ptPixelLocation: POINT { x, y },
                ..Default::default()
            },
            penMask: PEN_MASK_PRESSURE | PEN_MASK_TILT_X | PEN_MASK_TILT_Y,
            pressure: pressure_1024(pressure),
            tiltX: tilt_x.clamp(-90, 90) as i32,
            tiltY: tilt_y.clamp(-90, 90) as i32,
            ..Default::default()
        };
        if buttons & 8 != 0 {
            info.penFlags |= PEN_FLAG_BARREL;
        }
        if buttons & 16 != 0 {
            info.penFlags |= PEN_FLAG_ERASER;
        }
        let ti = POINTER_TYPE_INFO { r#type: PT_PEN, Anonymous: POINTER_TYPE_INFO_0 { penInfo: info } };
        let ok = inject(dev, &[ti], "pen");
        match phase {
            PointerPhase::Down | PointerPhase::Move => self.pen_down = Some((x + vx, y + vy)),
            PointerPhase::Up => self.pen_down = None,
            _ => {}
        }
        match phase {
            PointerPhase::Down | PointerPhase::Move | PointerPhase::Hover => self.pen_in_range = true,
            PointerPhase::Wheel => self.pen_in_range = false,
            PointerPhase::Up => {}
        }
        ok
    }

    /// Inject a touch event. All active contacts are sent together, as Windows expects.
    pub fn touch(&mut self, id: u8, phase: PointerPhase, x: i32, y: i32, pressure: f32) -> bool {
        let Some(dev) = self.touch else { return false };
        let (vx, vy) = virtual_origin();
        let (x, y) = (x - vx, y - vy);
        let c = Contact { x, y, pressure: pressure_1024(pressure.max(0.1)) };
        let event = match phase {
            PointerPhase::Down => {
                if self.contacts.len() >= 10 && !self.contacts.contains_key(&id) {
                    return false;
                }
                self.contacts.insert(id, c);
                POINTER_FLAG_DOWN | POINTER_FLAG_INRANGE | POINTER_FLAG_INCONTACT
            }
            PointerPhase::Move => {
                if !self.contacts.contains_key(&id) {
                    return false;
                }
                self.contacts.insert(id, c);
                POINTER_FLAG_UPDATE | POINTER_FLAG_INRANGE | POINTER_FLAG_INCONTACT
            }
            PointerPhase::Up => {
                if self.contacts.insert(id, c).is_none() {
                    self.contacts.remove(&id);
                    return false;
                }
                POINTER_FLAG_UP
            }
            _ => return false,
        };

        let mut frame: Vec<POINTER_TYPE_INFO> = Vec::with_capacity(self.contacts.len());
        let mut ids: Vec<u8> = self.contacts.keys().copied().collect();
        ids.sort_unstable();
        for cid in ids {
            let ct = self.contacts[&cid];
            let flags =
                if cid == id { event } else { POINTER_FLAG_UPDATE | POINTER_FLAG_INRANGE | POINTER_FLAG_INCONTACT };
            let info = POINTER_TOUCH_INFO {
                pointerInfo: POINTER_INFO {
                    pointerType: PT_TOUCH,
                    pointerId: cid as u32,
                    pointerFlags: flags,
                    ptPixelLocation: POINT { x: ct.x, y: ct.y },
                    ..Default::default()
                },
                touchFlags: TOUCH_FLAG_NONE,
                touchMask: TOUCH_MASK_CONTACTAREA | TOUCH_MASK_ORIENTATION | TOUCH_MASK_PRESSURE,
                rcContact: RECT { left: ct.x - 2, top: ct.y - 2, right: ct.x + 2, bottom: ct.y + 2 },
                orientation: 90,
                pressure: ct.pressure,
                ..Default::default()
            };
            frame.push(POINTER_TYPE_INFO { r#type: PT_TOUCH, Anonymous: POINTER_TYPE_INFO_0 { touchInfo: info } });
        }
        let ok = inject(dev, &frame, "touch");
        if phase == PointerPhase::Up {
            self.contacts.remove(&id);
        }
        ok
    }

    /// Lift everything (session end or client vanished) so nothing stays "pressed".
    pub fn release_all(&mut self) {
        if let Some((x, y)) = self.pen_down {
            self.pen(PointerPhase::Up, x, y, 0.0, 0, 0, 0);
        }
        let ids: Vec<u8> = self.contacts.keys().copied().collect();
        for id in ids {
            let c = self.contacts[&id];
            self.touch(id, PointerPhase::Up, c.x, c.y, 0.0);
        }
    }
}
