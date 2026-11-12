//! Turns client pointer/key messages into OS input on the host.
//!
//! Backends, best first:
//! - Windows: `SendInput` scancodes for keys; synthetic pen and touch devices for pen/touch.
//! - Linux: a `uinput` virtual keyboard, touchscreen and pen tablet (needs /dev/uinput access).
//! - Anywhere else, or when the native path is unavailable: enigo (mouse + common keys), with
//!   touch and pen emulated as a left-button mouse.

#[cfg(not(windows))]
mod fallback;
#[cfg(target_os = "linux")]
pub mod linux;
mod scancodes;
#[cfg(windows)]
mod win;

use dd_proto::{Message, PointerPhase, PointerSource};
#[cfg(not(windows))]
use enigo::Coordinate;
#[cfg(not(windows))]
use enigo::Keyboard;
use enigo::{Axis, Button, Direction, Enigo, Mouse, Settings};
use std::collections::HashSet;

/// Where the captured area sits on the host desktop.
#[derive(Clone, Copy, Debug)]
pub struct Target {
    pub origin: (i32, i32),
    pub size: (u32, u32),
    /// Full desktop size, needed to scale into absolute-axis devices (Linux). (0, 0) = unknown.
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    pub desktop: (u32, u32),
}

/// Add `delta` to the carried fraction and return the whole steps now due (the rest stays in `rest`).
/// Garbage (NaN, huge values) cannot flood the host with scroll events.
fn whole_steps(rest: &mut f32, delta: f32) -> i32 {
    let delta = if delta.is_finite() { delta.clamp(-100.0, 100.0) } else { 0.0 };
    let total = *rest + delta;
    let steps = total.trunc();
    *rest = total - steps;
    steps as i32
}

/// What became of a touch or pen sample offered to the native backend.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Native {
    /// Injected.
    Handled,
    /// The backend exists but this sample failed or does not apply (a stale id, no free slot, a
    /// refused first sample): drop it. Replaying it as a mouse click would be a stray click.
    Dropped,
    /// No native device: emulate with the mouse.
    Unavailable,
}

pub struct Injector {
    enigo: Option<Enigo>,
    target: Target,
    buttons: u8,
    /// Fractions of a scroll step not yet sent (smooth-scrolling touchpads report less than one line).
    wheel_rest: (f32, f32),
    pressed_keys: HashSet<u16>,
    /// The single touch/pen contact currently emulated as a mouse, if any.
    emulated_contact: Option<u8>,
    #[cfg(windows)]
    win: Option<win::Pointers>,
    #[cfg(target_os = "linux")]
    uinput: Option<linux::Uinput>,
    /// Wayland portal session carrying keyboard, pointer and touch (when it has input).
    #[cfg(target_os = "linux")]
    portal: Option<std::sync::Arc<crate::portal::Portal>>,
    #[cfg(target_os = "linux")]
    portal_touches: HashSet<u8>,
    /// Geometry kept current by a background poller (uinput only), so a monitor added or removed
    /// mid-session does not leave absolute input mapped to a stale desktop.
    #[cfg(target_os = "linux")]
    layout: Option<LayoutWatch>,
}

/// Polls the desktop layout off the input path: a layout query runs `xrandr`/`hyprctl`, which
/// would add tens of milliseconds to pen events if done inline.
#[cfg(target_os = "linux")]
struct LayoutWatch {
    latest: std::sync::Arc<std::sync::Mutex<Target>>,
    stop: std::sync::Arc<std::sync::atomic::AtomicBool>,
}

#[cfg(target_os = "linux")]
impl LayoutWatch {
    fn start(target: Target, output: Option<String>) -> Self {
        use std::sync::atomic::{AtomicBool, Ordering};
        use std::sync::{Arc, Mutex};
        use std::time::{Duration, Instant};
        let latest = Arc::new(Mutex::new(target));
        let stop = Arc::new(AtomicBool::new(false));
        let (slot, stop_flag) = (latest.clone(), stop.clone());
        std::thread::spawn(move || {
            let mut generation = crate::display::layout_generation();
            let mut last = Instant::now() - Duration::from_secs(10);
            while !stop_flag.load(Ordering::Relaxed) {
                std::thread::sleep(Duration::from_millis(100));
                let now_generation = crate::display::layout_generation();
                if now_generation == generation && last.elapsed() < Duration::from_secs(2) {
                    continue;
                }
                generation = now_generation;
                last = Instant::now();
                let current = *slot.lock().unwrap_or_else(|e| e.into_inner());
                let refreshed = Self::query(current, output.as_deref());
                *slot.lock().unwrap_or_else(|e| e.into_inner()) = refreshed;
            }
        });
        Self { latest, stop }
    }

    fn query(target: Target, output: Option<&str>) -> Target {
        if crate::wayland_display::active() {
            match crate::wayland_display::outputs() {
                Ok(outputs) => linux::target_for_wayland(target, &outputs, output),
                Err(_) => target,
            }
        } else {
            match crate::linux_display::query() {
                Ok(query) => linux::target_for_x11(target, &query, output),
                Err(_) => target,
            }
        }
    }

    fn current(&self) -> Target {
        *self.latest.lock().unwrap_or_else(|e| e.into_inner())
    }
}

#[cfg(target_os = "linux")]
impl Drop for LayoutWatch {
    fn drop(&mut self) {
        self.stop.store(true, std::sync::atomic::Ordering::Relaxed);
    }
}

impl Injector {
    pub fn new(
        target: Target,
        portal: crate::display::PortalHandle,
        #[cfg(target_os = "linux")] output: Option<String>,
    ) -> Self {
        #[cfg(target_os = "linux")]
        let layout = portal.is_none().then(|| LayoutWatch::start(target, output));
        #[cfg(not(target_os = "linux"))]
        let _ = portal;
        let enigo = match Enigo::new(&Settings::default()) {
            Ok(e) => Some(e),
            Err(e) => {
                eprintln!("mouse injection unavailable: {e}");
                None
            }
        };
        #[cfg(windows)]
        let win = {
            let p = win::Pointers::new();
            match &p {
                Some(p) => eprintln!("input: native pen={} touch={}", p.has_pen(), p.has_touch()),
                None => eprintln!("input: native pen/touch unavailable, emulating with the mouse"),
            }
            p
        };
        #[cfg(target_os = "linux")]
        let uinput = match linux::Uinput::new() {
            Ok(u) => {
                eprintln!("input: using /dev/uinput (keyboard, touch, pen)");
                Some(u)
            }
            Err(e) => {
                eprintln!("input: /dev/uinput unavailable ({e}); using basic keys and mouse emulation");
                None
            }
        };
        Injector {
            enigo,
            target,
            buttons: 0,
            wheel_rest: (0.0, 0.0),
            pressed_keys: HashSet::new(),
            emulated_contact: None,
            #[cfg(windows)]
            win,
            #[cfg(target_os = "linux")]
            uinput,
            #[cfg(target_os = "linux")]
            portal: portal.filter(|p| p.has_input),
            #[cfg(target_os = "linux")]
            portal_touches: HashSet::new(),
            #[cfg(target_os = "linux")]
            layout,
        }
    }

    #[cfg(target_os = "linux")]
    fn refresh_target(&mut self) {
        if let Some(watch) = &self.layout {
            self.target = watch.current();
        }
    }

    fn absolute(&self, x: f32, y: f32) -> (i32, i32) {
        let (w, h) = self.target.size;
        (
            self.target.origin.0 + (x.clamp(0.0, 1.0) * w.saturating_sub(1) as f32).round() as i32,
            self.target.origin.1 + (y.clamp(0.0, 1.0) * h.saturating_sub(1) as f32).round() as i32,
        )
    }

    pub fn handle(&mut self, m: &Message) {
        match *m {
            Message::Pointer { id, source, phase, x, y, pressure, buttons, tilt_x, tilt_y, wheel_x, wheel_y } => {
                #[cfg(target_os = "linux")]
                self.refresh_target();
                let (ax, ay) = self.absolute(x, y);
                #[cfg(target_os = "linux")]
                if self.portal_pointer(id, source, phase, ax, ay, buttons, wheel_x, wheel_y) {
                    return;
                }
                match source {
                    PointerSource::Mouse => self.mouse(phase, ax, ay, buttons, wheel_x, wheel_y),
                    PointerSource::Touch | PointerSource::Pen => {
                        if self.native_pointer(id, source, phase, ax, ay, pressure, buttons, tilt_x, tilt_y)
                            == Native::Unavailable
                        {
                            self.emulate_with_mouse(id, phase, ax, ay);
                        }
                    }
                }
            }
            Message::Key { code, down } => self.key(code, down),
            _ => {}
        }
    }

    /// Wayland portal pointer/touch. Returns false when the portal does not handle this event (no
    /// portal, or a pen while `uinput` can do real pen events).
    #[cfg(target_os = "linux")]
    #[allow(clippy::too_many_arguments)]
    fn portal_pointer(
        &mut self,
        id: u8,
        source: PointerSource,
        phase: PointerPhase,
        ax: i32,
        ay: i32,
        buttons: u8,
        wheel_x: f32,
        wheel_y: f32,
    ) -> bool {
        let Some(p) = self.portal.clone() else { return false };
        let (x, y) = (ax as f64, ay as f64);
        match source {
            PointerSource::Pen if self.uinput.is_some() => return false,
            PointerSource::Mouse => {
                if phase == PointerPhase::Wheel {
                    let sx = whole_steps(&mut self.wheel_rest.0, wheel_x);
                    let sy = whole_steps(&mut self.wheel_rest.1, wheel_y);
                    p.pointer_scroll(sx, -sy); // portal: positive = down
                } else {
                    p.pointer_move(x, y);
                    for (bit, code) in [(1u8, 0x110), (2, 0x111), (4, 0x112)] {
                        let (was, is) = (self.buttons & bit != 0, buttons & bit != 0);
                        if was != is {
                            p.pointer_button(code, is);
                        }
                    }
                    self.buttons = buttons;
                }
            }
            // Touch, and a pen without a native device: as a finger.
            _ => match phase {
                PointerPhase::Down => {
                    self.portal_touches.insert(id);
                    p.touch_down(id as u32, x, y);
                }
                PointerPhase::Move => {
                    if self.portal_touches.contains(&id) {
                        p.touch_move(id as u32, x, y);
                    }
                }
                PointerPhase::Up if self.portal_touches.remove(&id) => p.touch_up(id as u32),
                _ => {} // hover and wheel have no touch meaning
            },
        }
        true
    }

    #[allow(clippy::too_many_arguments)]
    fn native_pointer(
        &mut self,
        id: u8,
        source: PointerSource,
        phase: PointerPhase,
        ax: i32,
        ay: i32,
        pressure: f32,
        buttons: u8,
        tilt_x: i8,
        tilt_y: i8,
    ) -> Native {
        #[cfg(windows)]
        if let Some(w) = self.win.as_mut() {
            let (present, injected) = match source {
                PointerSource::Pen => (w.has_pen(), w.pen(phase, ax, ay, pressure, buttons, tilt_x, tilt_y)),
                _ => (w.has_touch(), w.touch(id, phase, ax, ay, pressure)),
            };
            return match (present, injected) {
                (false, _) => Native::Unavailable,
                (true, true) => Native::Handled,
                (true, false) => Native::Dropped,
            };
        }
        #[cfg(target_os = "linux")]
        if let Some(u) = self.uinput.as_mut() {
            let (nx, ny) = linux::to_axis(ax, ay, self.target);
            return if u.pointer(id, source, phase, nx, ny, pressure, buttons, tilt_x, tilt_y) {
                Native::Handled
            } else {
                Native::Dropped
            };
        }
        let _ = (id, source, phase, ax, ay, pressure, buttons, tilt_x, tilt_y);
        Native::Unavailable
    }

    /// Single-finger fallback: the first contact acts as the left mouse button.
    fn emulate_with_mouse(&mut self, id: u8, phase: PointerPhase, ax: i32, ay: i32) {
        match phase {
            PointerPhase::Down if self.emulated_contact.is_none() => {
                self.emulated_contact = Some(id);
                self.mouse(PointerPhase::Down, ax, ay, 1, 0.0, 0.0);
            }
            PointerPhase::Move | PointerPhase::Up if self.emulated_contact == Some(id) => {
                if phase == PointerPhase::Up {
                    self.emulated_contact = None;
                    self.mouse(PointerPhase::Up, ax, ay, 0, 0.0, 0.0);
                } else {
                    self.mouse(PointerPhase::Move, ax, ay, 1, 0.0, 0.0);
                }
            }
            _ => {}
        }
    }

    fn mouse(&mut self, phase: PointerPhase, ax: i32, ay: i32, buttons: u8, wheel_x: f32, wheel_y: f32) {
        // Wayland: the compositor ignores XTest, so drive an absolute uinput pointer instead.
        #[cfg(target_os = "linux")]
        if let Some(u) = self.uinput.as_mut().filter(|u| u.has_mouse()) {
            if phase == PointerPhase::Wheel {
                let x = whole_steps(&mut self.wheel_rest.0, wheel_x);
                let y = whole_steps(&mut self.wheel_rest.1, wheel_y);
                u.mouse_event(None, None, (x, y));
            } else {
                let pos = linux::to_axis(ax, ay, self.target);
                u.mouse_event(Some(pos), Some(buttons), (0, 0));
            }
            self.buttons = buttons;
            return;
        }
        // Windows moves the cursor itself, so a missing enigo only costs buttons and scrolling.
        #[cfg(windows)]
        if phase != PointerPhase::Wheel {
            let _ = win::move_cursor(ax, ay);
        }
        let Some(enigo) = self.enigo.as_mut() else { return };
        #[cfg(not(windows))]
        if phase != PointerPhase::Wheel {
            let _ = enigo.move_mouse(ax, ay, Coordinate::Abs);
        }
        if phase == PointerPhase::Wheel {
            let (y_steps, x_steps) =
                (whole_steps(&mut self.wheel_rest.1, wheel_y), whole_steps(&mut self.wheel_rest.0, wheel_x));
            if y_steps != 0 {
                let _ = enigo.scroll(-y_steps, Axis::Vertical);
            }
            if x_steps != 0 {
                let _ = enigo.scroll(x_steps, Axis::Horizontal);
            }
            return;
        }
        // Diff the button mask so Down/Up/Move all stay consistent.
        let (old, new) = (self.buttons, buttons);
        for (bit, btn) in [(1u8, Button::Left), (2, Button::Right), (4, Button::Middle)] {
            if old & bit == 0 && new & bit != 0 {
                let _ = enigo.button(btn, Direction::Press);
            } else if old & bit != 0 && new & bit == 0 {
                let _ = enigo.button(btn, Direction::Release);
            }
        }
        self.buttons = new;
    }

    fn key(&mut self, code: u16, down: bool) {
        #[cfg(target_os = "linux")]
        if code == 0x46 {
            // Linux maps HID Print Screen to KEY_SYSRQ. Alt+SysRq can reboot
            // the host, so never expose the kernel SysRq interface remotely.
            return;
        }
        #[cfg(target_os = "linux")]
        {
            let is_ctrl = matches!(code, 0xE0 | 0xE4);
            let is_alt = matches!(code, 0xE2 | 0xE6);
            let ctrl_alt_after = Self::linux_ctrl_alt_active(&self.pressed_keys, code, down);
            let is_function = (0x3A..=0x45).contains(&code);
            if down && is_function && ctrl_alt_after {
                return;
            }
            if down && (is_ctrl || is_alt) && ctrl_alt_after {
                let held_functions: Vec<u16> =
                    self.pressed_keys.iter().copied().filter(|key| (0x3A..=0x45).contains(key)).collect();
                for key in held_functions {
                    self.pressed_keys.remove(&key);
                    self.inject_key(key, false);
                }
            }
        }
        if down {
            self.pressed_keys.insert(code);
        } else {
            self.pressed_keys.remove(&code);
        }
        self.inject_key(code, down);
    }

    fn inject_key(&mut self, code: u16, down: bool) {
        #[cfg(windows)]
        {
            let _ = win::key(code, down);
        }
        #[cfg(not(windows))]
        {
            #[cfg(target_os = "linux")]
            if let (Some(p), Some(k)) = (self.portal.as_ref(), scancodes::linux_key_hid(code)) {
                p.key(k as i32, down);
                return;
            }
            #[cfg(target_os = "linux")]
            if let Some(u) = self.uinput.as_mut() {
                if u.key(code, down) {
                    return;
                }
            }
            if let (Some(enigo), Some(k)) = (self.enigo.as_mut(), fallback::hid_to_key(code)) {
                let _ = enigo.key(k, if down { Direction::Press } else { Direction::Release });
            }
        }
    }

    #[cfg(target_os = "linux")]
    fn linux_ctrl_alt_active(held: &HashSet<u16>, code: u16, down: bool) -> bool {
        let ctrl = if matches!(code, 0xE0 | 0xE4) { down } else { held.contains(&0xE0) || held.contains(&0xE4) };
        let alt = if matches!(code, 0xE2 | 0xE6) { down } else { held.contains(&0xE2) || held.contains(&0xE6) };
        ctrl && alt
    }

    /// Release every key, button and contact still held, so a dropped client cannot leave them stuck.
    pub fn release_all(&mut self) {
        #[cfg(target_os = "linux")]
        if let Some(p) = self.portal.clone() {
            for id in std::mem::take(&mut self.portal_touches) {
                p.touch_up(id as u32);
            }
            for (bit, code) in [(1u8, 0x110), (2, 0x111), (4, 0x112)] {
                if self.buttons & bit != 0 {
                    p.pointer_button(code, false);
                }
            }
            self.buttons = 0;
        }
        for code in std::mem::take(&mut self.pressed_keys) {
            #[cfg(target_os = "linux")]
            if let (Some(p), Some(k)) = (self.portal.as_ref(), scancodes::linux_key_hid(code)) {
                p.key(k as i32, false);
                continue;
            }
            #[cfg(windows)]
            {
                let _ = win::key(code, false);
            }
            #[cfg(not(windows))]
            {
                #[cfg(target_os = "linux")]
                if let Some(u) = self.uinput.as_mut() {
                    if u.key(code, false) {
                        continue;
                    }
                }
                if let (Some(enigo), Some(k)) = (self.enigo.as_mut(), fallback::hid_to_key(code)) {
                    let _ = enigo.key(k, Direction::Release);
                }
            }
        }
        if self.buttons != 0 {
            if let Some(enigo) = self.enigo.as_mut() {
                for (bit, btn) in [(1u8, Button::Left), (2, Button::Right), (4, Button::Middle)] {
                    if self.buttons & bit != 0 {
                        let _ = enigo.button(btn, Direction::Release);
                    }
                }
            }
            self.buttons = 0;
        }
        #[cfg(windows)]
        if let Some(w) = self.win.as_mut() {
            w.release_all();
        }
        #[cfg(target_os = "linux")]
        if let Some(u) = self.uinput.as_mut() {
            u.release_all();
        }
    }
}

impl Drop for Injector {
    fn drop(&mut self) {
        self.release_all();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn small_scroll_deltas_add_up() {
        let mut rest = 0.0;
        let total: i32 = (0..10).map(|_| whole_steps(&mut rest, 0.25)).sum();
        assert_eq!(total, 2); // 10 x 0.25 = 2.5, and the half is carried
        assert_eq!(rest, 0.5);
        assert_eq!(whole_steps(&mut 0.0, -2.0), -2);
    }

    #[test]
    fn scroll_garbage_is_harmless() {
        let mut rest = 0.0;
        assert_eq!(whole_steps(&mut rest, f32::NAN), 0);
        assert_eq!(whole_steps(&mut rest, f32::INFINITY), 0);
        assert_eq!(whole_steps(&mut rest, 1e30), 100);
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn ctrl_alt_function_key_chords_are_blocked() {
        let mut held = HashSet::new();
        assert!(!Injector::linux_ctrl_alt_active(&held, 0xE0, true));
        held.insert(0xE0);
        assert!(Injector::linux_ctrl_alt_active(&held, 0xE2, true));
        held.insert(0xE2);
        assert!(Injector::linux_ctrl_alt_active(&held, 0x3B, true));
        assert!(!Injector::linux_ctrl_alt_active(&held, 0xE2, false));
    }
}
