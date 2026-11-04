//! USB HID keyboard usage (page 0x07) -> PC set-1 scancode.
//!
//! Windows `SendInput` takes these scancodes directly (with the extended flag for the
//! `0xE0` prefixed keys). On Linux the base (non-extended) set-1 codes are identical to the
//! kernel's `KEY_*` numbers; extended keys have their own numbers (see [`linux_key`]).

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Scan {
    pub code: u8,
    /// Sent with an `0xE0` prefix.
    pub ext: bool,
}

const fn s(code: u8) -> Option<Scan> {
    Some(Scan { code, ext: false })
}
const fn e(code: u8) -> Option<Scan> {
    Some(Scan { code, ext: true })
}

pub fn scan(hid: u16) -> Option<Scan> {
    match hid {
        // a..z
        0x04 => s(0x1E),
        0x05 => s(0x30),
        0x06 => s(0x2E),
        0x07 => s(0x20),
        0x08 => s(0x12),
        0x09 => s(0x21),
        0x0A => s(0x22),
        0x0B => s(0x23),
        0x0C => s(0x17),
        0x0D => s(0x24),
        0x0E => s(0x25),
        0x0F => s(0x26),
        0x10 => s(0x32),
        0x11 => s(0x31),
        0x12 => s(0x18),
        0x13 => s(0x19),
        0x14 => s(0x10),
        0x15 => s(0x13),
        0x16 => s(0x1F),
        0x17 => s(0x14),
        0x18 => s(0x16),
        0x19 => s(0x2F),
        0x1A => s(0x11),
        0x1B => s(0x2D),
        0x1C => s(0x15),
        0x1D => s(0x2C),
        // 1..9, 0
        0x1E..=0x27 => s(0x02 + (hid - 0x1E) as u8),
        0x28 => s(0x1C),                             // Enter
        0x29 => s(0x01),                             // Esc
        0x2A => s(0x0E),                             // Backspace
        0x2B => s(0x0F),                             // Tab
        0x2C => s(0x39),                             // Space
        0x2D => s(0x0C),                             // -
        0x2E => s(0x0D),                             // =
        0x2F => s(0x1A),                             // [
        0x30 => s(0x1B),                             // ]
        0x31 => s(0x2B),                             // backslash
        0x33 => s(0x27),                             // ;
        0x34 => s(0x28),                             // '
        0x35 => s(0x29),                             // `
        0x36 => s(0x33),                             // ,
        0x37 => s(0x34),                             // .
        0x38 => s(0x35),                             // /
        0x39 => s(0x3A),                             // Caps Lock
        0x3A..=0x43 => s(0x3B + (hid - 0x3A) as u8), // F1..F10
        0x44 => s(0x57),                             // F11
        0x45 => s(0x58),                             // F12
        0x46 => e(0x37),                             // Print Screen
        0x47 => s(0x46),                             // Scroll Lock
        0x49 => e(0x52),                             // Insert
        0x4A => e(0x47),                             // Home
        0x4B => e(0x49),                             // Page Up
        0x4C => e(0x53),                             // Delete
        0x4D => e(0x4F),                             // End
        0x4E => e(0x51),                             // Page Down
        0x4F => e(0x4D),                             // Right
        0x50 => e(0x4B),                             // Left
        0x51 => e(0x50),                             // Down
        0x52 => e(0x48),                             // Up
        0x53 => e(0x45),                             // Num Lock (E0 45; plain 45 is Pause on Windows)
        0x54 => e(0x35),                             // Keypad /
        0x55 => s(0x37),                             // Keypad *
        0x56 => s(0x4A),                             // Keypad -
        0x57 => s(0x4E),                             // Keypad +
        0x58 => e(0x1C),                             // Keypad Enter
        0x59 => s(0x4F),
        0x5A => s(0x50),
        0x5B => s(0x51), // Keypad 1 2 3
        0x5C => s(0x4B),
        0x5D => s(0x4C),
        0x5E => s(0x4D), // 4 5 6
        0x5F => s(0x47),
        0x60 => s(0x48),
        0x61 => s(0x49), // 7 8 9
        0x62 => s(0x52), // Keypad 0
        0x63 => s(0x53), // Keypad .
        0x64 => s(0x56), // ISO extra key (< > |)
        0x65 => e(0x5D), // Menu
        0x67 => s(0x59), // Keypad =
        // Japanese keyboard keys
        0x87 => s(0x73), // Ro (\ _)
        0x88 => s(0x70), // Katakana / Hiragana
        0x89 => s(0x7D), // Yen
        0x8A => s(0x79), // Henkan (Convert)
        0x8B => s(0x7B), // Muhenkan (Non-convert)
        // F13..F23 (F24 is 0x76)
        0x68..=0x72 => s(0x64 + (hid - 0x68) as u8),
        0x73 => s(0x76), // F24
        0xE0 => s(0x1D), // Left Ctrl
        0xE1 => s(0x2A), // Left Shift
        0xE2 => s(0x38), // Left Alt
        0xE3 => e(0x5B), // Left Win/Super
        0xE4 => e(0x1D), // Right Ctrl
        0xE5 => s(0x36), // Right Shift
        0xE6 => e(0x38), // Right Alt / AltGr
        0xE7 => e(0x5C), // Right Win/Super
        _ => None,
    }
}

/// Linux `KEY_*` number for a scancode. Base keys share numbers with set 1; extended keys differ.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
pub fn linux_key(sc: Scan) -> Option<u16> {
    if !sc.ext {
        return Some(sc.code as u16);
    }
    Some(match sc.code {
        0x45 => 69,  // Num Lock
        0x37 => 99,  // SYSRQ / Print Screen
        0x52 => 110, // Insert
        0x47 => 102, // Home
        0x49 => 104, // Page Up
        0x53 => 111, // Delete
        0x4F => 107, // End
        0x51 => 109, // Page Down
        0x4D => 106, // Right
        0x4B => 105, // Left
        0x50 => 108, // Down
        0x48 => 103, // Up
        0x35 => 98,  // Keypad /
        0x1C => 96,  // Keypad Enter
        0x5D => 127, // Compose / Menu
        0x1D => 97,  // Right Ctrl
        0x38 => 100, // Right Alt
        0x5B => 125, // Left Meta
        0x5C => 126, // Right Meta
        _ => return None,
    })
}

/// Linux `KEY_*` number for a HID usage. Keypad = and F13..F24 cannot go through [`linux_key`]: their
/// Windows scancodes (0x64..) collide with unrelated base keys in the kernel's numbering.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
pub fn linux_key_hid(hid: u16) -> Option<u16> {
    match hid {
        0x48 => Some(119),                       // KEY_PAUSE (Windows has no single scancode for it)
        0x87 => Some(89),                        // KEY_RO
        0x88 => Some(90),                        // KEY_KATAKANAHIRAGANA
        0x89 => Some(124),                       // KEY_YEN
        0x8A => Some(92),                        // KEY_HENKAN
        0x8B => Some(94),                        // KEY_MUHENKAN
        0x67 => Some(117),                       // KEY_KPEQUAL
        0x68..=0x6F => Some(183 + (hid - 0x68)), // KEY_F13..KEY_F20
        0x70..=0x73 => Some(191 + (hid - 0x70)), // KEY_F21..KEY_F24
        _ => scan(hid).and_then(linux_key),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn known_codes() {
        assert_eq!(scan(0x04), Some(Scan { code: 0x1E, ext: false })); // A
        assert_eq!(scan(0x28), Some(Scan { code: 0x1C, ext: false })); // Enter
        assert_eq!(scan(0x27), Some(Scan { code: 0x0B, ext: false })); // 0
        assert_eq!(scan(0x1E), Some(Scan { code: 0x02, ext: false })); // 1
        assert_eq!(scan(0x52), Some(Scan { code: 0x48, ext: true })); // Up
        assert_eq!(scan(0xFFFF), None);
    }

    #[test]
    fn linux_numbers_match_the_kernel() {
        let key = |hid| linux_key(scan(hid).unwrap()).unwrap();
        assert_eq!(key(0x04), 30); // KEY_A
        assert_eq!(key(0x29), 1); // KEY_ESC
        assert_eq!(key(0x52), 103); // KEY_UP
        assert_eq!(key(0x4C), 111); // KEY_DELETE
        assert_eq!(key(0xE4), 97); // KEY_RIGHTCTRL
        assert_eq!(key(0xE3), 125); // KEY_LEFTMETA
    }

    #[test]
    fn num_lock_and_function_row_extension() {
        assert_eq!(linux_key_hid(0x53), Some(69)); // KEY_NUMLOCK
        assert_eq!(scan(0x53), Some(Scan { code: 0x45, ext: true }));
        assert_eq!(linux_key_hid(0x68), Some(183)); // KEY_F13
        assert_eq!(linux_key_hid(0x73), Some(194)); // KEY_F24
        assert_eq!(scan(0x73), Some(Scan { code: 0x76, ext: false }));
        assert_eq!(linux_key_hid(0x67), Some(117)); // KEY_KPEQUAL
        assert_eq!(linux_key_hid(0x89), Some(124)); // KEY_YEN
        assert_eq!(linux_key_hid(0x48), Some(119)); // KEY_PAUSE
    }

    #[test]
    fn no_two_usages_share_a_key() {
        let mut seen = std::collections::HashSet::new();
        for hid in 0u16..=0xFF {
            if let Some(sc) = scan(hid) {
                assert!(seen.insert((sc.code, sc.ext)), "usage {hid:#x} duplicates {sc:?}");
            }
        }
    }
}
