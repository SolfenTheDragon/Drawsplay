//go:build linux

package main

// Minimal X11 wire-protocol client that reports screen activity: DAMAGE on
// the composite overlay window (or the root when there is no compositor) and
// pointer movement. It exists so the capture pipeline can be paused while
// nothing changes (see x11_idle_gate_linux.go). No dependencies: the
// handful of requests needed are encoded by hand.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	x11OpQueryExtension = 98
	x11OpQueryPointer   = 38

	x11DamageRawRectangles = 0
	x11PointerPollInterval = 4 * time.Millisecond
	x11DamageSubtractEvery = 250 * time.Millisecond
)

type x11Rect struct{ X, Y, W, H int }

func (r x11Rect) intersects(x, y, w, h int) bool {
	return x < r.X+r.W && x+w > r.X && y < r.Y+r.H && y+h > r.Y
}

func (r x11Rect) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

type x11Conn struct {
	c    net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
	root uint32
	base uint32
	next uint32
}

func (x *x11Conn) newID() uint32 {
	id := x.base | x.next
	x.next++
	return id
}

func (x *x11Conn) send(b []byte) error {
	x.wmu.Lock()
	defer x.wmu.Unlock()
	_, err := x.c.Write(b)
	return err
}

func pad4(n int) int { return (n + 3) &^ 3 }

// parseX11Display splits ":N" / ":N.S" into the display number. Remote
// displays are not supported.
func parseX11Display(display string) (int, error) {
	if !strings.HasPrefix(display, ":") {
		return 0, fmt.Errorf("unsupported X display %q (only local displays)", display)
	}
	num := strings.TrimPrefix(display, ":")
	if i := strings.IndexByte(num, '.'); i >= 0 {
		num = num[:i]
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, fmt.Errorf("bad X display %q: %w", display, err)
	}
	return n, nil
}

// readXauthority returns the MIT-MAGIC-COOKIE-1 for a local display, or nil.
func readXauthority(displayNum int) []byte {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	host, _ := os.Hostname()
	want := strconv.Itoa(displayNum)
	var wild []byte
	for len(data) >= 2 {
		family := binary.BigEndian.Uint16(data)
		data = data[2:]
		var fields [4][]byte
		for i := range fields {
			if len(data) < 2 {
				return wild
			}
			n := int(binary.BigEndian.Uint16(data))
			data = data[2:]
			if len(data) < n {
				return wild
			}
			fields[i] = data[:n]
			data = data[n:]
		}
		addr, num, name, cookie := string(fields[0]), string(fields[1]), string(fields[2]), fields[3]
		if name != "MIT-MAGIC-COOKIE-1" || (num != "" && num != want) {
			continue
		}
		if family == 256 && addr == host {
			return cookie
		}
		if family == 65535 && wild == nil {
			wild = cookie
		}
	}
	return wild
}

func dialX11(display string) (*x11Conn, error) {
	num, err := parseX11Display(display)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", fmt.Sprintf("/tmp/.X11-unix/X%d", num), 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to X display %s: %w", display, err)
	}
	x := &x11Conn{c: conn, r: bufio.NewReaderSize(conn, 64*1024)}
	if err := x.handshake(num); err != nil {
		conn.Close()
		return nil, err
	}
	return x, nil
}

func (x *x11Conn) handshake(displayNum int) error {
	var authName, authData []byte
	if cookie := readXauthority(displayNum); cookie != nil {
		authName, authData = []byte("MIT-MAGIC-COOKIE-1"), cookie
	}
	req := make([]byte, 12, 12+pad4(len(authName))+pad4(len(authData)))
	req[0] = 'l'
	binary.LittleEndian.PutUint16(req[2:], 11)
	binary.LittleEndian.PutUint16(req[6:], uint16(len(authName)))
	binary.LittleEndian.PutUint16(req[8:], uint16(len(authData)))
	req = append(req, authName...)
	req = append(req, make([]byte, pad4(len(authName))-len(authName))...)
	req = append(req, authData...)
	req = append(req, make([]byte, pad4(len(authData))-len(authData))...)
	if err := x.send(req); err != nil {
		return fmt.Errorf("X11 handshake: %w", err)
	}
	x.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer x.c.SetReadDeadline(time.Time{})
	head := make([]byte, 8)
	if _, err := io.ReadFull(x.r, head); err != nil {
		return fmt.Errorf("X11 handshake: %w", err)
	}
	body := make([]byte, int(binary.LittleEndian.Uint16(head[6:]))*4)
	if _, err := io.ReadFull(x.r, body); err != nil {
		return fmt.Errorf("X11 handshake: %w", err)
	}
	if head[0] != 1 {
		return fmt.Errorf("X11 server refused the connection: %s", strings.TrimSpace(string(body[:min(int(head[1]), len(body))])))
	}
	if len(body) < 32 {
		return errors.New("X11 handshake: short setup reply")
	}
	x.base = binary.LittleEndian.Uint32(body[4:])
	vendorLen := int(binary.LittleEndian.Uint16(body[16:]))
	numFormats := int(body[21])
	screen := 32 + pad4(vendorLen) + numFormats*8
	if len(body) < screen+4 {
		return errors.New("X11 handshake: short screen data")
	}
	x.root = binary.LittleEndian.Uint32(body[screen:])
	return nil
}

// readMessage reads one 32-byte message (plus the extra words of a reply).
func (x *x11Conn) readMessage() (msg []byte, err error) {
	msg = make([]byte, 32)
	if _, err = io.ReadFull(x.r, msg); err != nil {
		return nil, err
	}
	if msg[0] == 1 {
		if extra := int(binary.LittleEndian.Uint32(msg[4:])) * 4; extra > 0 {
			rest := make([]byte, extra)
			if _, err = io.ReadFull(x.r, rest); err != nil {
				return nil, err
			}
			msg = append(msg, rest...)
		}
	}
	return msg, nil
}

// call sends a request that has a reply and waits for it, ignoring events.
func (x *x11Conn) call(req []byte) ([]byte, error) {
	if err := x.send(req); err != nil {
		return nil, err
	}
	x.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer x.c.SetReadDeadline(time.Time{})
	for {
		msg, err := x.readMessage()
		if err != nil {
			return nil, err
		}
		switch msg[0] {
		case 1:
			return msg, nil
		case 0:
			return nil, fmt.Errorf("X11 error code %d (request opcode %d)", msg[1], binary.LittleEndian.Uint16(msg[8:]))
		}
	}
}

// extension returns an extension's major opcode and first event code.
func (x *x11Conn) extension(name string) (major, firstEvent byte, ok bool, err error) {
	req := make([]byte, 8+pad4(len(name)))
	req[0] = x11OpQueryExtension
	binary.LittleEndian.PutUint16(req[2:], uint16(len(req)/4))
	binary.LittleEndian.PutUint16(req[4:], uint16(len(name)))
	copy(req[8:], name)
	rep, err := x.call(req)
	if err != nil {
		return 0, 0, false, err
	}
	return rep[9], rep[10], rep[8] != 0, nil
}

func extRequest(major, minor byte, words int) []byte {
	req := make([]byte, words*4)
	req[0], req[1] = major, minor
	binary.LittleEndian.PutUint16(req[2:], uint16(words))
	return req
}

// x11ActivityWatcher calls onActivity whenever the watched rectangle is
// damaged or the pointer moves in or out of it.
type x11ActivityWatcher struct {
	x    *x11Conn
	done chan struct{}
	once sync.Once
}

func newX11ActivityWatcher(display string, rect x11Rect, onActivity func()) (*x11ActivityWatcher, error) {
	x, err := dialX11(display)
	if err != nil {
		return nil, err
	}
	w := &x11ActivityWatcher{x: x, done: make(chan struct{})}
	fail := func(err error) (*x11ActivityWatcher, error) {
		x.c.Close()
		return nil, err
	}

	// Under a compositor the screen is drawn into the composite overlay
	// window, which the root window's damage does not cover.
	target := x.root
	if major, _, ok, err := x.extension("Composite"); err != nil {
		return fail(err)
	} else if ok {
		ver := extRequest(major, 0, 3)
		binary.LittleEndian.PutUint32(ver[4:], 0)
		binary.LittleEndian.PutUint32(ver[8:], 4)
		if _, err := x.call(ver); err != nil {
			return fail(fmt.Errorf("Composite version: %w", err))
		}
		req := extRequest(major, 7, 2)
		binary.LittleEndian.PutUint32(req[4:], x.root)
		if rep, err := x.call(req); err == nil {
			target = binary.LittleEndian.Uint32(rep[8:])
		}
	}

	major, firstEvent, ok, err := x.extension("DAMAGE")
	if err != nil {
		return fail(err)
	}
	if !ok {
		return fail(errors.New("X server has no DAMAGE extension"))
	}
	ver := extRequest(major, 0, 3)
	binary.LittleEndian.PutUint32(ver[4:], 1)
	binary.LittleEndian.PutUint32(ver[8:], 1)
	if _, err := x.call(ver); err != nil {
		return fail(fmt.Errorf("DAMAGE version: %w", err))
	}
	damage := x.newID()
	create := extRequest(major, 1, 4)
	binary.LittleEndian.PutUint32(create[4:], damage)
	binary.LittleEndian.PutUint32(create[8:], target)
	create[12] = x11DamageRawRectangles
	if err := x.send(create); err != nil {
		return fail(err)
	}

	go w.readLoop(rect, firstEvent, onActivity)
	go w.pollLoop(major, damage)
	return w, nil
}

func (w *x11ActivityWatcher) readLoop(rect x11Rect, firstEvent byte, onActivity func()) {
	var lastX, lastY int
	havePointer := false
	for {
		msg, err := w.x.readMessage()
		if err != nil {
			w.close()
			return
		}
		switch {
		case msg[0] == 1: // QueryPointer reply
			px := int(int16(binary.LittleEndian.Uint16(msg[16:])))
			py := int(int16(binary.LittleEndian.Uint16(msg[18:])))
			if havePointer && (px != lastX || py != lastY) &&
				(rect.contains(px, py) || rect.contains(lastX, lastY)) {
				onActivity()
			}
			lastX, lastY, havePointer = px, py, true
		case msg[0]&0x7f == firstEvent: // DamageNotify
			ax := int(int16(binary.LittleEndian.Uint16(msg[16:])))
			ay := int(int16(binary.LittleEndian.Uint16(msg[18:])))
			aw := int(binary.LittleEndian.Uint16(msg[20:]))
			ah := int(binary.LittleEndian.Uint16(msg[22:]))
			if rect.intersects(ax, ay, aw, ah) {
				onActivity()
			}
		}
	}
}

func (w *x11ActivityWatcher) pollLoop(damageMajor byte, damage uint32) {
	poll := time.NewTicker(x11PointerPollInterval)
	defer poll.Stop()
	subtract := time.NewTicker(x11DamageSubtractEvery)
	defer subtract.Stop()
	ptr := make([]byte, 8)
	ptr[0] = x11OpQueryPointer
	binary.LittleEndian.PutUint16(ptr[2:], 2)
	binary.LittleEndian.PutUint32(ptr[4:], w.x.root)
	sub := extRequest(damageMajor, 2, 4)
	binary.LittleEndian.PutUint32(sub[4:], damage)
	for {
		select {
		case <-w.done:
			return
		case <-poll.C:
			if w.x.send(ptr) != nil {
				w.close()
				return
			}
		case <-subtract.C:
			if w.x.send(sub) != nil {
				w.close()
				return
			}
		}
	}
}

// closed reports whether the watcher has stopped (connection lost or close).
func (w *x11ActivityWatcher) closed() <-chan struct{} { return w.done }

func (w *x11ActivityWatcher) close() {
	w.once.Do(func() {
		close(w.done)
		w.x.c.Close()
	})
}
