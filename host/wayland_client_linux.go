//go:build linux

package main

// A minimal Wayland client: the wire protocol and the registry, enough to
// bind a few globals and exchange their messages without libwayland (the
// host stays a static Go binary). Objects are plain ids; each one's events
// go to a wlHandler.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// wlRoundtripTimeout bounds a wait for the compositor to answer.
const wlRoundtripTimeout = 5 * time.Second

// wlMaxFDs is how many file descriptors one message may carry (libwayland's
// limit, too).
const wlMaxFDs = 28

var errWlClosed = errors.New("Wayland connection closed")

// wlHandler receives one object's events. It runs on the connection's reader
// goroutine, so it must not block, nor wait for other events.
type wlHandler func(op uint16, m *wlMessage)

// wlConn is a client connection to a Wayland compositor.
type wlConn struct {
	conn *net.UnixConn
	wmu  sync.Mutex // serializes writes

	mu sync.Mutex
	// handlers has an entry for every id in use; a nil handler ignores the
	// events of an object destroyed but not yet deleted by the compositor.
	handlers map[uint32]wlHandler
	// The compositor requires new ids to be the lowest unused or freed ones
	// (libwayland-server rejects gaps), so ids it deleted are reused first.
	free []uint32
	next uint32

	done      chan struct{}
	err       error // set before done is closed
	closeOnce sync.Once
}

// waylandSocketPath is the compositor's socket, from WAYLAND_DISPLAY (an
// absolute path, or a name in XDG_RUNTIME_DIR).
func waylandSocketPath() (string, error) {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		name = "wayland-0"
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set")
	}
	return filepath.Join(dir, name), nil
}

func dialWayland() (*wlConn, error) {
	path, err := waylandSocketPath()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("connect to the Wayland compositor: %w", err)
	}
	return newWlConn(conn), nil
}

func newWlConn(conn *net.UnixConn) *wlConn {
	c := &wlConn{conn: conn, handlers: map[uint32]wlHandler{}, next: 2, done: make(chan struct{})}
	c.handlers[1] = c.handleDisplay // wl_display
	go c.readLoop()
	return c
}

// Done is closed when the connection ends.
func (c *wlConn) Done() <-chan struct{} { return c.done }

// Err is why the connection ended, or nil while it is open.
func (c *wlConn) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

func (c *wlConn) Close() { c.fail(errWlClosed) }

func (c *wlConn) fail(err error) {
	c.closeOnce.Do(func() {
		c.err = err
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *wlConn) handleDisplay(op uint16, m *wlMessage) {
	switch op {
	case 0: // error
		obj, code, msg := m.Uint(), m.Uint(), m.Str()
		c.fail(fmt.Errorf("Wayland protocol error on object %d (code %d): %s", obj, code, msg))
	case 1: // delete_id
		id := m.Uint()
		c.mu.Lock()
		if _, ok := c.handlers[id]; ok && id != 1 {
			delete(c.handlers, id)
			c.free = append(c.free, id)
		}
		c.mu.Unlock()
	}
}

// newID allocates an id for an object a request is about to create; the
// caller holds wmu until the request is sent, as the compositor requires new
// ids in the order allocated.
func (c *wlConn) newID(h wlHandler) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var id uint32
	if n := len(c.free); n > 0 {
		id, c.free = c.free[n-1], c.free[:n-1]
	} else {
		id = c.next
		c.next++
	}
	c.handlers[id] = h
	return id
}

// destroy sends id's destructor request; the object's later events are
// ignored, and its id is reused once the compositor deletes it.
func (c *wlConn) destroy(id uint32, op uint16, args ...wlArg) error {
	c.mu.Lock()
	if _, ok := c.handlers[id]; ok {
		c.handlers[id] = nil
	}
	c.mu.Unlock()
	return c.request(id, op, args...)
}

// wlArg is one request argument.
type wlArg interface {
	appendTo(b []byte, fds []int) ([]byte, []int)
}

type (
	wlUint   uint32 // uint, object and new_id arguments
	wlInt    int32
	wlFixed  float64
	wlString string
	wlArray  []byte
	wlFD     int
)

func (v wlUint) appendTo(b []byte, fds []int) ([]byte, []int) {
	return binary.NativeEndian.AppendUint32(b, uint32(v)), fds
}

func (v wlInt) appendTo(b []byte, fds []int) ([]byte, []int) {
	return binary.NativeEndian.AppendUint32(b, uint32(v)), fds
}

func (v wlFixed) appendTo(b []byte, fds []int) ([]byte, []int) {
	return binary.NativeEndian.AppendUint32(b, uint32(int32(math.Round(float64(v)*256)))), fds
}

func (v wlString) appendTo(b []byte, fds []int) ([]byte, []int) {
	b = binary.NativeEndian.AppendUint32(b, uint32(len(v)+1))
	b = append(b, v...)
	b = append(b, 0)
	return wlPad(b), fds
}

func (v wlArray) appendTo(b []byte, fds []int) ([]byte, []int) {
	b = binary.NativeEndian.AppendUint32(b, uint32(len(v)))
	return wlPad(append(b, v...)), fds
}

func (v wlFD) appendTo(b []byte, fds []int) ([]byte, []int) {
	return b, append(fds, int(v))
}

func wlPad(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// request sends request op of object id.
func (c *wlConn) request(id uint32, op uint16, args ...wlArg) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.sendLocked(id, op, args)
}

// create sends request op of object id, one that creates an object whose
// events go to h; args builds the request's arguments around the new
// object's id. It returns that id.
func (c *wlConn) create(h wlHandler, id uint32, op uint16, args func(newID uint32) []wlArg) (uint32, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	nid := c.newID(h)
	return nid, c.sendLocked(id, op, args(nid))
}

func (c *wlConn) sendLocked(id uint32, op uint16, args []wlArg) error {
	b := make([]byte, 8, 64)
	var fds []int
	for _, a := range args {
		b, fds = a.appendTo(b, fds)
	}
	if len(b) > math.MaxUint16 || len(fds) > wlMaxFDs {
		return errors.New("Wayland request too large")
	}
	binary.NativeEndian.PutUint32(b[0:], id)
	binary.NativeEndian.PutUint32(b[4:], uint32(len(b))<<16|uint32(op))
	if err := c.Err(); err != nil {
		return err
	}
	var oob []byte
	if len(fds) > 0 {
		oob = unix.UnixRights(fds...)
	}
	n, _, err := c.conn.WriteMsgUnix(b, oob, nil)
	if err == nil && n < len(b) {
		_, err = c.conn.Write(b[n:])
	}
	if err != nil {
		c.fail(fmt.Errorf("Wayland write: %w", err))
		return c.Err()
	}
	return nil
}

func (c *wlConn) readLoop() {
	pending := make([]byte, 0, 64*1024)
	chunk := make([]byte, 32*1024)
	oob := make([]byte, unix.CmsgSpace(wlMaxFDs*4))
	for {
		n, oobn, _, _, err := c.conn.ReadMsgUnix(chunk, oob)
		if oobn > 0 {
			closeWlFDs(oob[:oobn])
		}
		if n > 0 {
			pending = append(pending, chunk[:n]...)
			rest, ok := c.dispatch(pending)
			if !ok {
				c.fail(errors.New("malformed Wayland message"))
				return
			}
			pending = pending[:copy(pending, rest)]
		}
		switch {
		case err != nil:
			c.fail(fmt.Errorf("Wayland connection: %w", err))
			return
		case n == 0:
			c.fail(errors.New("the Wayland compositor closed the connection"))
			return
		}
	}
}

// dispatch hands every complete message in buf to its object's handler and
// returns what is left of buf.
func (c *wlConn) dispatch(buf []byte) ([]byte, bool) {
	for len(buf) >= 8 {
		id := binary.NativeEndian.Uint32(buf[0:])
		word := binary.NativeEndian.Uint32(buf[4:])
		size, op := int(word>>16), uint16(word&0xffff)
		if size < 8 || size%4 != 0 {
			return nil, false
		}
		if len(buf) < size {
			break
		}
		c.mu.Lock()
		h := c.handlers[id]
		c.mu.Unlock()
		if h != nil {
			h(op, &wlMessage{data: buf[8:size]})
		}
		buf = buf[size:]
	}
	return buf, true
}

// closeWlFDs closes the file descriptors a message carried: no event the
// host handles uses one.
func closeWlFDs(oob []byte) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, msg := range msgs {
		fds, err := unix.ParseUnixRights(&msg)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}
}

// roundtrip waits until the compositor has handled every request sent so
// far and sent their events. Not for handlers: it waits for the reader.
func (c *wlConn) roundtrip() error {
	done := make(chan struct{})
	var once sync.Once
	_, err := c.create(func(op uint16, _ *wlMessage) {
		if op == 0 { // wl_callback.done
			once.Do(func() { close(done) })
		}
	}, 1, 0, wlNewID) // wl_display.sync
	if err != nil {
		return err
	}
	t := time.NewTimer(wlRoundtripTimeout)
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-c.done:
		return c.Err()
	case <-t.C:
		return errors.New("the Wayland compositor did not answer")
	}
}

// wlNewID builds the arguments of a request whose only one is the new id.
func wlNewID(id uint32) []wlArg { return []wlArg{wlUint(id)} }

// wlMessage reads an event's arguments in order.
type wlMessage struct {
	data []byte
}

func (m *wlMessage) Uint() uint32 {
	if len(m.data) < 4 {
		m.data = nil
		return 0
	}
	v := binary.NativeEndian.Uint32(m.data)
	m.data = m.data[4:]
	return v
}

func (m *wlMessage) Int() int32 { return int32(m.Uint()) }

func (m *wlMessage) Fixed() float64 { return float64(m.Int()) / 256 }

// Str reads a string argument (a null string reads as "").
func (m *wlMessage) Str() string {
	b := m.Array()
	if len(b) == 0 {
		return ""
	}
	return string(b[:len(b)-1]) // without the terminating NUL
}

func (m *wlMessage) Array() []byte {
	n := int(m.Uint())
	padded := (n + 3) &^ 3
	if padded > len(m.data) {
		m.data = nil
		return nil
	}
	b := append([]byte(nil), m.data[:n]...)
	m.data = m.data[padded:]
	return b
}

// --- registry ------------------------------------------------------------------------

type wlGlobal struct {
	Name      uint32
	Interface string
	Version   uint32
}

type wlRegistry struct {
	c *wlConn

	mu      sync.Mutex
	id      uint32
	globals map[uint32]wlGlobal
	// watch hears of globals as they come and go, from the reader
	// goroutine.
	watch func(r *wlRegistry, g wlGlobal, added bool)
}

// getRegistry lists the compositor's globals. watch, when not nil, hears of
// every global (the initial ones included) as it comes and goes; it runs as
// a wlHandler does.
func (c *wlConn) getRegistry(watch func(r *wlRegistry, g wlGlobal, added bool)) (*wlRegistry, error) {
	r := &wlRegistry{c: c, globals: map[uint32]wlGlobal{}, watch: watch}
	// The globals may arrive before create returns; handle waits for id.
	r.mu.Lock()
	id, err := c.create(r.handle, 1, 1, wlNewID) // wl_display.get_registry
	r.id = id
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := c.roundtrip(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *wlRegistry) handle(op uint16, m *wlMessage) {
	switch op {
	case 0: // global
		g := wlGlobal{Name: m.Uint(), Interface: m.Str(), Version: m.Uint()}
		r.mu.Lock()
		r.globals[g.Name] = g
		r.mu.Unlock()
		if r.watch != nil {
			r.watch(r, g, true)
		}
	case 1: // global_remove
		name := m.Uint()
		r.mu.Lock()
		g, ok := r.globals[name]
		delete(r.globals, name)
		r.mu.Unlock()
		if ok && r.watch != nil {
			r.watch(r, g, false)
		}
	}
}

// find lists the globals of an interface, oldest first.
func (r *wlRegistry) find(iface string) []wlGlobal {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []wlGlobal
	for _, g := range r.globals {
		if g.Interface == iface {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// bind binds global g at version, or at g's version when that is lower,
// and returns the new object's id and version.
func (r *wlRegistry) bind(g wlGlobal, version uint32, h wlHandler) (uint32, uint32, error) {
	v := min(version, g.Version)
	r.mu.Lock()
	reg := r.id
	r.mu.Unlock()
	id, err := r.c.create(h, reg, 0, func(id uint32) []wlArg {
		return []wlArg{wlUint(g.Name), wlString(g.Interface), wlUint(v), wlUint(id)}
	})
	return id, v, err
}
