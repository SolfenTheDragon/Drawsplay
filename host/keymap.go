package main

// Android keycode → host keycode table (milestone M5, keyboard passthrough).
//
// The tablet forwards physical-keyboard events as "key" packets carrying the
// Android KeyEvent keycode. This table maps those to both host encodings so
// each platform injector can pick its column:
//
//	vk    — Windows virtual-key code (winuser.h VK_*), used by SendInput.
//	evdev — Linux input event code (linux/input-event-codes.h KEY_*), used
//	        by the uinput virtual keyboard.
//
// Keys not in the table fall back to per-character Unicode injection on
// Windows (packet "chars" field) and are dropped on Linux (logged once).
// The table is layout-independent where possible: it maps physical-ish key
// identities, letting the host keyboard layout decide the produced character
// (except for the Unicode fallback, which is literal).

// keyMapping is one Android keycode's host equivalents.
type keyMapping struct {
	vk    uint16 // Windows virtual-key code
	evdev uint16 // Linux KEY_* event code
}

// lookupKey resolves an Android keycode. ok=false means unmapped.
func lookupKey(androidKeycode int) (keyMapping, bool) {
	m, ok := androidKeycodes[androidKeycode]
	return m, ok
}

// Android KeyEvent.KEYCODE_* values → host codes.
var androidKeycodes = map[int]keyMapping{
	// Letters (KEYCODE_A..Z = 29..54; VK 'A'..'Z'; KEY_A.. per evdev layout).
	29: {0x41, 30}, // A
	30: {0x42, 48}, // B
	31: {0x43, 46}, // C
	32: {0x44, 32}, // D
	33: {0x45, 18}, // E
	34: {0x46, 33}, // F
	35: {0x47, 34}, // G
	36: {0x48, 35}, // H
	37: {0x49, 23}, // I
	38: {0x4A, 36}, // J
	39: {0x4B, 37}, // K
	40: {0x4C, 38}, // L
	41: {0x4D, 50}, // M
	42: {0x4E, 49}, // N
	43: {0x4F, 24}, // O
	44: {0x50, 25}, // P
	45: {0x51, 16}, // Q
	46: {0x52, 19}, // R
	47: {0x53, 31}, // S
	48: {0x54, 20}, // T
	49: {0x55, 22}, // U
	50: {0x56, 47}, // V
	51: {0x57, 17}, // W
	52: {0x58, 45}, // X
	53: {0x59, 21}, // Y
	54: {0x5A, 44}, // Z

	// Top-row digits (KEYCODE_0..9 = 7..16; VK '0'..'9').
	7:  {0x30, 11}, // 0
	8:  {0x31, 2},  // 1
	9:  {0x32, 3},  // 2
	10: {0x33, 4},  // 3
	11: {0x34, 5},  // 4
	12: {0x35, 6},  // 5
	13: {0x36, 7},  // 6
	14: {0x37, 8},  // 7
	15: {0x38, 9},  // 8
	16: {0x39, 10}, // 9

	// Whitespace / editing.
	62:  {0x20, 57},  // SPACE
	66:  {0x0D, 28},  // ENTER
	61:  {0x09, 15},  // TAB
	67:  {0x08, 14},  // DEL (backspace)
	112: {0x2E, 111}, // FORWARD_DEL (delete)
	111: {0x1B, 1},   // ESCAPE
	124: {0x2D, 110}, // INSERT

	// Modifiers.
	59:  {0xA0, 42},  // SHIFT_LEFT
	60:  {0xA1, 54},  // SHIFT_RIGHT
	113: {0xA2, 29},  // CTRL_LEFT
	114: {0xA3, 97},  // CTRL_RIGHT
	57:  {0xA4, 56},  // ALT_LEFT
	58:  {0xA5, 100}, // ALT_RIGHT
	117: {0x5B, 125}, // META_LEFT (Win/Super)
	118: {0x5C, 126}, // META_RIGHT
	115: {0x14, 58},  // CAPS_LOCK

	// Navigation.
	19:  {0x26, 103}, // DPAD_UP
	20:  {0x28, 108}, // DPAD_DOWN
	21:  {0x25, 105}, // DPAD_LEFT
	22:  {0x27, 106}, // DPAD_RIGHT
	122: {0x24, 102}, // MOVE_HOME
	123: {0x23, 107}, // MOVE_END
	92:  {0x21, 104}, // PAGE_UP
	93:  {0x22, 109}, // PAGE_DOWN

	// Function keys (KEYCODE_F1..F12 = 131..142).
	131: {0x70, 59}, // F1
	132: {0x71, 60}, // F2
	133: {0x72, 61}, // F3
	134: {0x73, 62}, // F4
	135: {0x74, 63}, // F5
	136: {0x75, 64}, // F6
	137: {0x76, 65}, // F7
	138: {0x77, 66}, // F8
	139: {0x78, 67}, // F9
	140: {0x79, 68}, // F10
	141: {0x7A, 87}, // F11
	142: {0x7B, 88}, // F12

	// Punctuation (US-layout OEM keys; host layout may remap).
	55: {0xBC, 51}, // COMMA
	56: {0xBE, 52}, // PERIOD
	69: {0xBD, 12}, // MINUS
	70: {0xBB, 13}, // EQUALS
	71: {0xDB, 26}, // LEFT_BRACKET
	72: {0xDD, 27}, // RIGHT_BRACKET
	73: {0xDC, 43}, // BACKSLASH
	74: {0xBA, 39}, // SEMICOLON
	75: {0xDE, 40}, // APOSTROPHE
	76: {0xBF, 53}, // SLASH
	68: {0xC0, 41}, // GRAVE

	// Numpad (KEYCODE_NUMPAD_0..9 = 144..153).
	144: {0x60, 82}, // NUMPAD_0
	145: {0x61, 79}, // NUMPAD_1
	146: {0x62, 80}, // NUMPAD_2
	147: {0x63, 81}, // NUMPAD_3
	148: {0x64, 75}, // NUMPAD_4
	149: {0x65, 76}, // NUMPAD_5
	150: {0x66, 77}, // NUMPAD_6
	151: {0x67, 71}, // NUMPAD_7
	152: {0x68, 72}, // NUMPAD_8
	153: {0x69, 73}, // NUMPAD_9
	154: {0x6F, 98}, // NUMPAD_DIVIDE
	155: {0x6A, 55}, // NUMPAD_MULTIPLY
	156: {0x6D, 74}, // NUMPAD_SUBTRACT
	157: {0x6B, 78}, // NUMPAD_ADD
	158: {0x6E, 83}, // NUMPAD_DOT
	160: {0x0D, 96}, // NUMPAD_ENTER
	143: {0x90, 69}, // NUM_LOCK

	// System keys.
	116: {0x91, 70},  // SCROLL_LOCK
	120: {0x2C, 99},  // SYSRQ (PrintScreen)
	121: {0x13, 119}, // BREAK (Pause)
}
