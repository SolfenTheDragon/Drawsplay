//! Last-resort keyboard mapping through enigo, used where no native backend is available
//! (for example Linux without access to /dev/uinput). Covers the common keys only, and
//! types characters through the host's layout rather than pressing physical keys.

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
        0x4A => Key::Home,
        0x4B => Key::PageUp,
        0x4C => Key::Delete,
        0x4D => Key::End,
        0x4E => Key::PageDown,
        0x4F => Key::RightArrow,
        0x50 => Key::LeftArrow,
        0x51 => Key::DownArrow,
        0x52 => Key::UpArrow,
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
        assert!(hid_to_key(0xFFFF).is_none());
    }
}
