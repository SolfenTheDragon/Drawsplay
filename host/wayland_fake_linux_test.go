//go:build linux

package main

import (
	"encoding/binary"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakeWl is a scripted Wayland compositor on the other end of a socketpair.
// It answers wl_display and wl_registry itself and hands every other request
// to the handler of the object's interface.
type fakeWl struct {
	t        *testing.T
	conn     *net.UnixConn
	clientFD int
	wmu      sync.Mutex

	mu       sync.Mutex
	globals  []wlGlobal
	registry uint32
	objects  map[uint32]string // live client objects by id
	versions map[uint32]uint32
	maxID    uint32
	// handlers serve requests by interface; onBind runs after a bind.
	handlers map[string]fakeWlHandler
	onBind   map[string]func(f *fakeWl, global, id, version uint32)
}

type fakeWlHandler func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int)

func newFakeWl(t *testing.T) *fakeWl {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeWl{
		t:        t,
		conn:     fdUnixConn(t, fds[0]),
		objects:  map[uint32]string{1: "wl_display"},
		versions: map[uint32]uint32{},
		maxID:    1,
		handlers: map[string]fakeWlHandler{},
		onBind:   map[string]func(*fakeWl, uint32, uint32, uint32){},
	}
	f.clientFD = fds[1]
	t.Cleanup(func() { _ = f.conn.Close() })
	return f
}

// connect starts serving and returns the client's connection. Globals and
// handlers set up before connect are in place for the first request.
func (f *fakeWl) connect() *wlConn {
	c := newWlConn(fdUnixConn(f.t, f.clientFD))
	go f.serve()
	f.t.Cleanup(c.Close)
	return c
}

func fdUnixConn(t *testing.T, fd int) *net.UnixConn {
	t.Helper()
	file := os.NewFile(uintptr(fd), "socketpair")
	c, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	return c.(*net.UnixConn)
}

func (f *fakeWl) handle(iface string, h fakeWlHandler) {
	f.mu.Lock()
	f.handlers[iface] = h
	f.mu.Unlock()
}

func (f *fakeWl) bound(iface string, h func(f *fakeWl, global, id, version uint32)) {
	f.mu.Lock()
	f.onBind[iface] = h
	f.mu.Unlock()
}

func (f *fakeWl) addGlobal(name uint32, iface string, version uint32) {
	f.mu.Lock()
	f.globals = append(f.globals, wlGlobal{Name: name, Interface: iface, Version: version})
	reg := f.registry
	f.mu.Unlock()
	if reg != 0 {
		f.send(reg, 0, wlUint(name), wlString(iface), wlUint(version))
	}
}

func (f *fakeWl) removeGlobal(name uint32) {
	f.mu.Lock()
	for i, g := range f.globals {
		if g.Name == name {
			f.globals = append(f.globals[:i], f.globals[i+1:]...)
			break
		}
	}
	reg := f.registry
	f.mu.Unlock()
	f.send(reg, 1, wlUint(name))
}

// send writes an event.
func (f *fakeWl) send(id uint32, op uint16, args ...wlArg) {
	b := make([]byte, 8, 64)
	for _, a := range args {
		b, _ = a.appendTo(b, nil)
	}
	binary.NativeEndian.PutUint32(b[0:], id)
	binary.NativeEndian.PutUint32(b[4:], uint32(len(b))<<16|uint32(op))
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = f.conn.Write(b)
}

// newObject records a client-created object, enforcing libwayland-server's
// rule that a new id is a free one no higher than the next unused one.
func (f *fakeWl) newObject(id uint32, iface string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, used := f.objects[id]; used || id == 0 || id > f.maxID+1 {
		f.t.Errorf("client created %s with invalid id %d (highest so far %d)", iface, id, f.maxID)
	}
	f.objects[id] = iface
	f.maxID = max(f.maxID, id)
}

// deleteObject destroys a client object and tells the client its id is free.
func (f *fakeWl) deleteObject(id uint32) {
	f.mu.Lock()
	delete(f.objects, id)
	f.mu.Unlock()
	f.send(1, 1, wlUint(id))
}

func (f *fakeWl) iface(id uint32) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[id]
}

func (f *fakeWl) version(id uint32) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.versions[id]
}

func (f *fakeWl) setVersion(id, version uint32) {
	f.mu.Lock()
	f.versions[id] = version
	f.mu.Unlock()
}

func (f *fakeWl) serve() {
	pending := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(wlMaxFDs*4))
	var fds []int
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	for {
		n, oobn, _, _, err := f.conn.ReadMsgUnix(chunk, oob)
		if oobn > 0 {
			msgs, _ := unix.ParseSocketControlMessage(oob[:oobn])
			for _, msg := range msgs {
				got, _ := unix.ParseUnixRights(&msg)
				fds = append(fds, got...)
			}
		}
		if err != nil || n == 0 {
			return
		}
		pending = append(pending, chunk[:n]...)
		for len(pending) >= 8 {
			id := binary.NativeEndian.Uint32(pending[0:])
			word := binary.NativeEndian.Uint32(pending[4:])
			size, op := int(word>>16), uint16(word&0xffff)
			if len(pending) < size {
				break
			}
			m := &wlMessage{data: append([]byte(nil), pending[8:size]...)}
			pending = pending[size:]
			fds = f.serveRequest(id, op, m, fds)
		}
	}
}

// serveRequest serves one request and returns the descriptors still unused.
func (f *fakeWl) serveRequest(id uint32, op uint16, m *wlMessage, fds []int) []int {
	switch iface := f.iface(id); {
	case id == 1 && op == 0: // sync
		cb := m.Uint()
		f.newObject(cb, "wl_callback")
		f.send(cb, 0, wlUint(7))
		f.deleteObject(cb)
	case id == 1 && op == 1: // get_registry
		reg := m.Uint()
		f.newObject(reg, "wl_registry")
		f.mu.Lock()
		f.registry = reg
		globals := append([]wlGlobal(nil), f.globals...)
		f.mu.Unlock()
		for _, g := range globals {
			f.send(reg, 0, wlUint(g.Name), wlString(g.Interface), wlUint(g.Version))
		}
	case iface == "wl_registry" && op == 0: // bind
		global, bound, version, newID := m.Uint(), m.Str(), m.Uint(), m.Uint()
		f.newObject(newID, bound)
		f.setVersion(newID, version)
		f.mu.Lock()
		hook := f.onBind[bound]
		f.mu.Unlock()
		if hook != nil {
			hook(f, global, newID, version)
		}
	case iface == "":
		f.t.Errorf("request %d to unknown object %d", op, id)
	default:
		f.mu.Lock()
		h := f.handlers[iface]
		f.mu.Unlock()
		if h == nil {
			f.t.Errorf("unexpected request %d to %s", op, iface)
			return fds
		}
		h(f, id, op, m, fds)
		return nil // the handler owns any descriptors
	}
	return fds
}

// fakeWlOutput describes an output the fake compositor advertises.
type fakeWlOutput struct {
	name                       string
	x, y, w, h                 int32 // logical
	modeW, modeH, scale, trans int32
}

// fakeWlOutputs advertises outputs as wl_output globals, with an
// xdg-output manager (global 90) at xdgVersion unless that is 0.
type fakeWlOutputs struct {
	f        *fakeWl
	mu       sync.Mutex
	outputs  map[uint32]fakeWlOutput // by global name
	wlObj    map[uint32][]uint32     // global -> wl_output ids
	xdgObj   map[uint32][]uint32     // global -> xdg_output ids
	globalOf map[uint32]uint32       // wl_output id -> global
}

func (f *fakeWl) serveOutputs(xdgVersion uint32) *fakeWlOutputs {
	s := &fakeWlOutputs{f: f, outputs: map[uint32]fakeWlOutput{}, wlObj: map[uint32][]uint32{}, xdgObj: map[uint32][]uint32{}, globalOf: map[uint32]uint32{}}
	f.bound("wl_output", func(f *fakeWl, global, id, version uint32) {
		s.mu.Lock()
		s.globalOf[id] = global
		s.wlObj[global] = append(s.wlObj[global], id)
		o := s.outputs[global]
		s.mu.Unlock()
		s.sendOutput(id, o)
	})
	f.handle("wl_output", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		if op == 0 { // release
			f.deleteObject(id)
		}
	})
	if xdgVersion == 0 {
		return s
	}
	f.addGlobal(90, "zxdg_output_manager_v1", xdgVersion)
	f.handle("zxdg_output_manager_v1", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		if op != 1 { // get_xdg_output
			return
		}
		xdg, output := m.Uint(), m.Uint()
		f.newObject(xdg, "zxdg_output_v1")
		f.setVersion(xdg, f.version(id))
		s.mu.Lock()
		global := s.globalOf[output]
		s.xdgObj[global] = append(s.xdgObj[global], xdg)
		o := s.outputs[global]
		s.mu.Unlock()
		s.sendXdg(xdg, o)
		if f.version(xdg) >= 3 {
			s.sendDone(output)
		}
	})
	f.handle("zxdg_output_v1", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		if op == 0 { // destroy
			f.deleteObject(id)
		}
	})
	return s
}

func (s *fakeWlOutputs) sendOutput(id uint32, o fakeWlOutput) {
	f := s.f
	f.send(id, 0, wlInt(o.x), wlInt(o.y), wlInt(0), wlInt(0), wlInt(0), wlString("make"), wlString("model"), wlInt(o.trans))
	f.send(id, 1, wlUint(wlOutputModeCurrent), wlInt(o.modeW), wlInt(o.modeH), wlInt(60000))
	v := f.version(id)
	if v >= 2 {
		f.send(id, 3, wlInt(max(o.scale, 1)))
	}
	if v >= 4 {
		f.send(id, 4, wlString(o.name))
	}
	s.sendDone(id)
}

func (s *fakeWlOutputs) sendDone(id uint32) {
	if s.f.version(id) >= 2 {
		s.f.send(id, 2)
	}
}

func (s *fakeWlOutputs) sendXdg(id uint32, o fakeWlOutput) {
	f := s.f
	f.send(id, 0, wlInt(o.x), wlInt(o.y))
	f.send(id, 1, wlInt(o.w), wlInt(o.h))
	v := f.version(id)
	if v >= 2 {
		f.send(id, 3, wlString(o.name))
	}
	if v < 3 {
		f.send(id, 2)
	}
}

// set adds or changes the output advertised as global.
func (s *fakeWlOutputs) set(global uint32, o fakeWlOutput) {
	s.mu.Lock()
	_, known := s.outputs[global]
	s.outputs[global] = o
	wl := append([]uint32(nil), s.wlObj[global]...)
	xdg := append([]uint32(nil), s.xdgObj[global]...)
	s.mu.Unlock()
	if !known {
		s.f.addGlobal(global, "wl_output", 4)
		return
	}
	for _, id := range xdg {
		s.f.send(id, 0, wlInt(o.x), wlInt(o.y))
		s.f.send(id, 1, wlInt(o.w), wlInt(o.h))
		if s.f.version(id) < 3 {
			s.f.send(id, 2)
		}
	}
	for _, id := range wl {
		s.f.send(id, 1, wlUint(wlOutputModeCurrent), wlInt(o.modeW), wlInt(o.modeH), wlInt(60000))
		s.sendDone(id)
	}
}

// remove withdraws the output advertised as global.
func (s *fakeWlOutputs) remove(global uint32) {
	s.mu.Lock()
	delete(s.outputs, global)
	s.mu.Unlock()
	s.f.removeGlobal(global)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
