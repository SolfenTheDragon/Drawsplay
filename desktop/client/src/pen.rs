//! Pen state that winit 0.30 does not report.
//!
//! On Windows, winit 0.30 turns a pen into a `WindowEvent::Touch` that carries only its pressure, so
//! the client could not tell a pen from a finger, and tilt, the barrel button and the eraser were
//! lost. [`windows_hook`] runs for every message winit takes from the queue. For each
//! `WM_POINTERDOWN/UPDATE/UP` from a pen it reads `POINTER_PEN_INFO` and records it under the
//! pointer id, which is also the id winit gives the matching `Touch`.
//!
//! winit 0.30 has no pen support on Linux (X11 and Wayland deliver the pen as a mouse), so nothing is
//! recorded there and [`sample`] always returns `None`.

use std::cell::RefCell;
use std::collections::HashMap;

/// Pen state read from the OS for one pointer.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct PenSample {
    /// 0..1, or `None` when the pen reports no pressure.
    pub pressure: Option<f32>,
    /// Degrees, -90..90. Positive x leans right, positive y leans toward the user (the protocol's
    /// and Windows' convention). 0/0 when the pen reports no tilt.
    pub tilt_x: i8,
    pub tilt_y: i8,
    /// The barrel (side) button is held.
    pub barrel: bool,
    /// The eraser end is in use or the eraser button is held.
    pub eraser: bool,
}

impl PenSample {
    /// `Message::Pointer` button bits while the pen touches: 1 contact, 8 barrel, 16 eraser.
    pub fn buttons(&self) -> u8 {
        1 | if self.barrel { 8 } else { 0 } | if self.eraser { 16 } else { 0 }
    }
}

thread_local! {
    // The hook and the window events both run on the event-loop thread.
    static PENS: RefCell<HashMap<u64, PenSample>> = RefCell::new(HashMap::new());
}

/// The latest pen state for this pointer id, or `None` if the pointer is not a pen.
pub fn sample(id: u64) -> Option<PenSample> {
    PENS.with(|p| p.borrow().get(&id).copied())
}

/// Forget a pointer once its contact has ended.
pub fn forget(id: u64) {
    PENS.with(|p| {
        p.borrow_mut().remove(&id);
    });
}

#[cfg_attr(not(windows), allow(dead_code))]
fn record(id: u64, s: Option<PenSample>) {
    PENS.with(|p| {
        let mut p = p.borrow_mut();
        match s {
            Some(s) => {
                p.insert(id, s);
            }
            None => {
                p.remove(&id);
            }
        }
    });
}

// Win32 values (winuser.h). Repeated here so the conversion can be tested on every platform; the
// Windows build checks them against the `windows` crate below.
#[cfg_attr(not(windows), allow(dead_code))]
const PEN_FLAG_BARREL: u32 = 0x1;
#[cfg_attr(not(windows), allow(dead_code))]
const PEN_FLAG_INVERTED: u32 = 0x2;
#[cfg_attr(not(windows), allow(dead_code))]
const PEN_FLAG_ERASER: u32 = 0x4;
#[cfg_attr(not(windows), allow(dead_code))]
const PEN_MASK_PRESSURE: u32 = 0x1;
#[cfg_attr(not(windows), allow(dead_code))]
const PEN_MASK_TILT_X: u32 = 0x4;
#[cfg_attr(not(windows), allow(dead_code))]
const PEN_MASK_TILT_Y: u32 = 0x8;

/// Convert the fields of a Win32 `POINTER_PEN_INFO`. Fields the pen does not report (per `mask`)
/// are left at their defaults.
#[cfg_attr(not(windows), allow(dead_code))]
pub(crate) fn from_pen_info(mask: u32, flags: u32, pressure: u32, tilt_x: i32, tilt_y: i32) -> PenSample {
    let tilt = |bit: u32, v: i32| if mask & bit != 0 { v.clamp(-90, 90) as i8 } else { 0 };
    PenSample {
        // POINTER_PEN_INFO.pressure is 0..1024.
        pressure: (mask & PEN_MASK_PRESSURE != 0).then(|| (pressure.min(1024) as f32) / 1024.0),
        tilt_x: tilt(PEN_MASK_TILT_X, tilt_x),
        tilt_y: tilt(PEN_MASK_TILT_Y, tilt_y),
        barrel: flags & PEN_FLAG_BARREL != 0,
        eraser: flags & (PEN_FLAG_INVERTED | PEN_FLAG_ERASER) != 0,
    }
}

/// winit message hook (`EventLoopBuilderExtWindows::with_msg_hook`): records pen state for
/// pointer messages and never consumes a message.
#[cfg(windows)]
pub fn windows_hook(msg: *const std::ffi::c_void) -> bool {
    use windows::Win32::UI::Input::Pointer::{GetPointerPenInfo, GetPointerType, POINTER_PEN_INFO};
    use windows::Win32::UI::WindowsAndMessaging::{
        MSG, POINTER_INPUT_TYPE, PT_PEN, WM_POINTERDOWN, WM_POINTERUP, WM_POINTERUPDATE,
    };

    const _: () = {
        use windows::Win32::UI::WindowsAndMessaging as w;
        assert!(w::PEN_FLAG_BARREL == PEN_FLAG_BARREL);
        assert!(w::PEN_FLAG_INVERTED == PEN_FLAG_INVERTED);
        assert!(w::PEN_FLAG_ERASER == PEN_FLAG_ERASER);
        assert!(w::PEN_MASK_PRESSURE == PEN_MASK_PRESSURE);
        assert!(w::PEN_MASK_TILT_X == PEN_MASK_TILT_X);
        assert!(w::PEN_MASK_TILT_Y == PEN_MASK_TILT_Y);
    };

    if msg.is_null() {
        return false;
    }
    // SAFETY: winit passes a pointer to the MSG it has just taken from the queue.
    let msg = unsafe { &*(msg as *const MSG) };
    if !matches!(msg.message, WM_POINTERDOWN | WM_POINTERUPDATE | WM_POINTERUP) {
        return false;
    }
    // GET_POINTERID_WPARAM: the low word.
    let id = (msg.wParam.0 & 0xFFFF) as u32;
    let mut kind = POINTER_INPUT_TYPE::default();
    // SAFETY: plain out-parameter calls for the pointer of the message being processed.
    let is_pen = unsafe { GetPointerType(id, &mut kind) }.is_ok() && kind == PT_PEN;
    let sample = if is_pen {
        let mut info = POINTER_PEN_INFO::default();
        unsafe { GetPointerPenInfo(id, &mut info) }
            .ok()
            .map(|_| from_pen_info(info.penMask, info.penFlags, info.pressure, info.tiltX, info.tiltY))
    } else {
        None
    };
    record(id as u64, sample);
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn converts_full_pen_info() {
        let s = from_pen_info(PEN_MASK_PRESSURE | PEN_MASK_TILT_X | PEN_MASK_TILT_Y, PEN_FLAG_BARREL, 512, -30, 45);
        assert_eq!(s, PenSample { pressure: Some(0.5), tilt_x: -30, tilt_y: 45, barrel: true, eraser: false });
        assert_eq!(s.buttons(), 1 | 8);
    }

    #[test]
    fn unreported_fields_stay_unknown() {
        // A pen without pressure or tilt sensors: values outside the mask are ignored.
        let s = from_pen_info(0, 0, 700, 20, 20);
        assert_eq!(s, PenSample::default());
        assert_eq!(s.buttons(), 1);
    }

    #[test]
    fn eraser_end_or_button_and_clamping() {
        let s = from_pen_info(PEN_MASK_TILT_X | PEN_MASK_PRESSURE, PEN_FLAG_INVERTED, 5000, 120, 0);
        assert!(s.eraser);
        assert_eq!(s.tilt_x, 90);
        assert_eq!(s.pressure, Some(1.0));
        assert_eq!(s.buttons(), 1 | 16);
        assert!(from_pen_info(0, PEN_FLAG_ERASER, 0, 0, 0).eraser);
    }

    #[test]
    fn records_and_forgets_by_pointer_id() {
        record(7, Some(PenSample { tilt_x: 10, ..Default::default() }));
        assert_eq!(sample(7).map(|s| s.tilt_x), Some(10));
        record(7, None);
        assert_eq!(sample(7), None);
        record(8, Some(PenSample::default()));
        forget(8);
        assert_eq!(sample(8), None);
    }
}
