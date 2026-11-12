//! Last-resort keyboard mapping through enigo, used where no native backend is available
//! (for example Linux without access to /dev/uinput). Covers the common, lock and numpad keys
//! only, and types characters through the host's layout rather than pressing physical keys.

use enigo::Key;

pub fn hid_to_key(code: u16) -> Option<Key> {
    Some(match code {
        0x04..=0x1D => Key::Unicode((b'a' + (code - 0x04) as u8) as char),
        0x1E..=0x26 => Key::Unicode((b'1' + (code - 0x1E) as u8) as char),
        0x27 => Key::Unicode('0'),
        0x28 => Key::Return,
        0x29 => Key::Escape,
        0x2A => Key::Backspace,
        0x2B => Key::Tab,
        0x2C => Key::Space,
        0x2D => Key::Unicode('-'),
        0x2E => Key::Unicode('='),
        0x2F => Key::Unicode('['),
        0x30 => Key::Unicode(']'),
        0x31 => Key::Unicode('\\'),
        0x33 => Key::Unicode(';'),
        0x34 => Key::Unicode('\''),
        0x35 => Key::Unicode('`'),
        0x36 => Key::Unicode(','),
        0x37 => Key::Unicode('.'),
        0x38 => Key::Unicode('/'),
        // Not on Linux: libxdo (enigo's X11 backend) picks a keymap level that holds Alt for
        // F1-F12 (they share keycodes with XF86Switch_VT_n), so F4 would arrive as Alt+F4.
        #[cfg(target_os = "macos")]
        0x3A..=0x45 => {
            const F: [Key; 12] = [
                Key::F1,
                Key::F2,
                Key::F3,
                Key::F4,
                Key::F5,
                Key::F6,
                Key::F7,
                Key::F8,
                Key::F9,
                Key::F10,
                Key::F11,
                Key::F12,
            ];
            F[(code - 0x3A) as usize]
        }
        #[cfg(all(unix, not(target_os = "macos")))]
        0x39 => Key::CapsLock,
        #[cfg(all(unix, not(target_os = "macos")))]
        0x46 => Key::PrintScr,
        #[cfg(all(unix, not(target_os = "macos")))]
        0x47 => Key::ScrollLock,
        #[cfg(all(unix, not(target_os = "macos")))]
        0x48 => Key::Pause,
        #[cfg(all(unix, not(target_os = "macos")))]
        0x49 => Key::Insert,
        #[cfg(all(unix, not(target_os = "macos")))]
        0x53 => Key::Numlock,
        0x4A => Key::Home,
        0x4B => Key::PageUp,
        0x4C => Key::Delete,
        0x4D => Key::End,
        0x4E => Key::PageDown,
        0x4F => Key::RightArrow,
        0x50 => Key::LeftArrow,
        0x51 => Key::DownArrow,
        0x52 => Key::UpArrow,
        // Numpad keys type their character, independent of the host's NumLock state.
        0x54 => Key::Unicode('/'),
        0x55 => Key::Unicode('*'),
        0x56 => Key::Unicode('-'),
        0x57 => Key::Unicode('+'),
        0x58 => Key::Return,
        0x59..=0x61 => Key::Unicode((b'1' + (code - 0x59) as u8) as char),
        0x62 => Key::Unicode('0'),
        0x63 => Key::Unicode('.'),
        0xE0 | 0xE4 => Key::Control,
        0xE1 | 0xE5 => Key::Shift,
        0xE2 | 0xE6 => Key::Alt,
        0xE3 | 0xE7 => Key::Meta,
        _ => return None,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mapping() {
        assert!(matches!(hid_to_key(0x04), Some(Key::Unicode('a'))));
        assert!(matches!(hid_to_key(0x27), Some(Key::Unicode('0'))));
        assert!(matches!(hid_to_key(0x4F), Some(Key::RightArrow)));
        #[cfg(target_os = "linux")]
        assert!(hid_to_key(0x3D).is_none(), "F4 must not become Alt+F4 through libxdo");
        assert!(matches!(hid_to_key(0x59), Some(Key::Unicode('1'))));
        assert!(matches!(hid_to_key(0x61), Some(Key::Unicode('9'))));
        #[cfg(target_os = "linux")]
        assert!(matches!(hid_to_key(0x49), Some(Key::Insert)));
        assert!(hid_to_key(0xFFFF).is_none());
    }
}
