//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// nudgeX11PointerAfterCapture repeats the nudge while the capture pipeline
// starts (and falls back across encoders), since that also blanks the cursor.
func nudgeX11PointerAfterCapture() {
	for _, d := range []time.Duration{0, 1500 * time.Millisecond, 3 * time.Second, 5 * time.Second} {
		time.Sleep(d)
		nudgeX11Pointer()
	}
}

// nudgeX11Pointer moves the pointer by one pixel and back. Muffin leaves the
// pointer blank (a 1x1 transparent cursor) after the virtual output and the
// Drawsplay touch devices appear, until the next pointer motion event.
func nudgeX11Pointer() {
	if err := x11MovePointerRelative(os.Getenv("DISPLAY"), 1, 0, -1, 0); err != nil {
		log.Printf("pointer nudge failed: %v", err)
	}
}

// x11MovePointerRelative sends XTest relative motion requests over the X
// socket, so no external tool is needed. Offsets are relative to the pointer.
func x11MovePointerRelative(display string, moves ...int16) error {
	if display == "" {
		return errors.New("DISPLAY is not set")
	}
	host, rest, found := strings.Cut(display, ":")
	if !found || (host != "" && host != "unix") {
		return fmt.Errorf("unsupported DISPLAY %q", display)
	}
	num, _, _ := strings.Cut(rest, ".")
	if _, err := strconv.Atoi(num); err != nil {
		return fmt.Errorf("unsupported DISPLAY %q", display)
	}
	conn, err := net.DialTimeout("unix", "/tmp/.X11-unix/X"+num, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	name, data := x11AuthCookie(num)
	pad := func(n int) int { return (4 - n%4) % 4 }
	var req bytes.Buffer
	req.Write([]byte{'B', 0})
	_ = binary.Write(&req, binary.BigEndian, [4]uint16{11, 0, uint16(len(name)), uint16(len(data))})
	req.Write([]byte{0, 0})
	req.WriteString(name)
	req.Write(make([]byte, pad(len(name))))
	req.Write(data)
	req.Write(make([]byte, pad(len(data))))
	if _, err := conn.Write(req.Bytes()); err != nil {
		return err
	}
	var head [8]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if head[0] != 1 {
		return errors.New("X server refused the connection")
	}
	if _, err := io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint16(head[6:8]))*4); err != nil {
		return err
	}
	// XTest motion is real input motion, which is what clears Muffin's blank
	// cursor; XWarpPointer does not.
	xtest, err := x11QueryExtension(conn, "XTEST")
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(moves); i += 2 {
		var w bytes.Buffer
		w.Write([]byte{xtest, 2, 0, 9, 6, 1, 0, 0})                // FakeInput, 9 words: MotionNotify, relative
		_ = binary.Write(&w, binary.BigEndian, [3]uint32{0, 0, 0}) // time, root, pad
		w.Write(make([]byte, 4))
		_ = binary.Write(&w, binary.BigEndian, [2]int16{moves[i], moves[i+1]})
		w.Write(make([]byte, 8))
		if _, err := conn.Write(w.Bytes()); err != nil {
			return err
		}
	}
	// A round trip makes the server finish the warps and report any error
	// (X errors are asynchronous) before the connection closes.
	if _, err := conn.Write([]byte{43, 0, 0, 1}); err != nil { // GetInputFocus
		return err
	}
	for {
		var ev [32]byte
		if _, err := io.ReadFull(conn, ev[:]); err != nil {
			return err
		}
		switch ev[0] {
		case 0:
			return fmt.Errorf("X error code %d on pointer request", ev[1])
		case 1:
			return nil
		}
	}
}

// x11AuthCookie returns the MIT-MAGIC-COOKIE-1 for a local display, or empty
// values when none is found (servers using host-based access).
func x11AuthCookie(num string) (string, []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	hostname, _ := os.Hostname()
	read := func(b []byte) ([]byte, []byte, bool) {
		if len(b) < 2 {
			return nil, nil, false
		}
		n := int(binary.BigEndian.Uint16(b))
		if len(b) < 2+n {
			return nil, nil, false
		}
		return b[2 : 2+n], b[2+n:], true
	}
	for len(raw) > 2 {
		family := binary.BigEndian.Uint16(raw)
		var addr, number, name, data []byte
		var ok bool
		rest := raw[2:]
		if addr, rest, ok = read(rest); !ok {
			return "", nil
		}
		if number, rest, ok = read(rest); !ok {
			return "", nil
		}
		if name, rest, ok = read(rest); !ok {
			return "", nil
		}
		if data, rest, ok = read(rest); !ok {
			return "", nil
		}
		raw = rest
		local := family == 256 && string(addr) == hostname || family == 65535
		if local && string(name) == "MIT-MAGIC-COOKIE-1" && (len(number) == 0 || string(number) == num) {
			return string(name), data
		}
	}
	return "", nil
}

// x11QueryExtension returns the major opcode of an X extension.
func x11QueryExtension(conn net.Conn, name string) (byte, error) {
	var q bytes.Buffer
	words := (len(name) + 3) / 4
	q.Write([]byte{98, 0})
	_ = binary.Write(&q, binary.BigEndian, [3]uint16{uint16(2 + words), uint16(len(name)), 0})
	q.WriteString(name)
	q.Write(make([]byte, words*4-len(name)))
	if _, err := conn.Write(q.Bytes()); err != nil {
		return 0, err
	}
	var rep [32]byte
	if _, err := io.ReadFull(conn, rep[:]); err != nil {
		return 0, err
	}
	if rep[0] != 1 || rep[8] == 0 {
		return 0, fmt.Errorf("X extension %s unavailable", name)
	}
	return rep[9], nil
}
