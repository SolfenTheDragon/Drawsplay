//go:build linux

package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestWlMessageRoundTripsArguments(t *testing.T) {
	var b []byte
	for _, a := range []wlArg{wlUint(7), wlInt(-3), wlFixed(1.5), wlString("abc"), wlString(""), wlArray([]byte{1, 2, 3, 4, 5}), wlString("four")} {
		b, _ = a.appendTo(b, nil)
	}
	if len(b)%4 != 0 {
		t.Fatalf("encoded length %d is not word aligned", len(b))
	}
	m := &wlMessage{data: b}
	if got := m.Uint(); got != 7 {
		t.Fatalf("uint = %d", got)
	}
	if got := m.Int(); got != -3 {
		t.Fatalf("int = %d", got)
	}
	if got := m.Fixed(); got != 1.5 {
		t.Fatalf("fixed = %v", got)
	}
	if got := m.Str(); got != "abc" {
		t.Fatalf("string = %q", got)
	}
	if got := m.Str(); got != "" {
		t.Fatalf("empty string = %q", got)
	}
	if got := m.Array(); string(got) != "\x01\x02\x03\x04\x05" {
		t.Fatalf("array = %v", got)
	}
	if got := m.Str(); got != "four" {
		t.Fatalf("string = %q", got)
	}
	if got := m.Uint(); got != 0 || m.data != nil {
		t.Fatalf("reading past the end = %d, rest %v", got, m.data)
	}
}

func TestWlConnListsGlobalsAndBinds(t *testing.T) {
	f := newFakeWl(t)
	f.addGlobal(5, "wl_shm", 1)
	f.addGlobal(3, "wl_output", 4)
	f.addGlobal(4, "wl_output", 3)
	c := f.connect()
	var mu sync.Mutex
	var seen []string
	reg, err := c.getRegistry(func(_ *wlRegistry, g wlGlobal, added bool) {
		mu.Lock()
		defer mu.Unlock()
		if added {
			seen = append(seen, g.Interface)
		} else {
			seen = append(seen, "-"+g.Interface)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := reg.find("wl_output")
	if len(outputs) != 2 || outputs[0].Name != 3 || outputs[1].Name != 4 {
		t.Fatalf("wl_output globals = %+v", outputs)
	}
	id, v, err := reg.bind(outputs[1], 4, nil)
	if err != nil || v != 3 {
		t.Fatalf("bind = %d v%d, %v; want version 3", id, v, err)
	}
	if err := c.roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := f.iface(id); got != "wl_output" || f.version(id) != 3 {
		t.Fatalf("compositor has object %d as %q v%d", id, got, f.version(id))
	}
	f.removeGlobal(5)
	if err := c.roundtrip(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(seen, ","); got != "wl_shm,wl_output,wl_output,-wl_shm" {
		t.Fatalf("registry events = %s", got)
	}
	if len(reg.find("wl_shm")) != 0 {
		t.Fatal("removed global still listed")
	}
}

// The compositor rejects a new id above the next unused one, or one still in
// use (the fake fails the test on either): ids are reused once deleted, and
// only then.
func TestWlConnReusesIDsOnlyAfterDeletion(t *testing.T) {
	f := newFakeWl(t)
	f.addGlobal(1, "test_object", 1)
	var keep uint32
	var mu sync.Mutex
	f.handle("test_object", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		mu.Lock()
		defer mu.Unlock()
		if id != keep { // keep stays destroyed but undeleted
			f.deleteObject(id)
		}
	})
	c := f.connect()
	reg, err := c.getRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	g := reg.find("test_object")[0]
	mu.Lock()
	keep, _, _ = reg.bind(g, 1, nil)
	mu.Unlock()
	if err := c.destroy(keep, 0); err != nil {
		t.Fatal(err)
	}
	f.send(keep, 0) // an event for a destroyed object is ignored
	for i := 0; i < 50; i++ {
		a, _, _ := reg.bind(g, 1, nil)
		b, _, _ := reg.bind(g, 1, nil)
		if a == b || a == keep || b == keep {
			t.Fatalf("ids %d and %d handed out with %d undeleted", a, b, keep)
		}
		_ = c.destroy(a, 0)
		_ = c.destroy(b, 0)
		if err := c.roundtrip(); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	highest := f.maxID
	f.mu.Unlock()
	if highest > 8 {
		t.Fatalf("ids grew to %d: deleted ids are not reused", highest)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}

func TestWlConnEndsOnProtocolError(t *testing.T) {
	f := newFakeWl(t)
	c := f.connect()
	if err := c.roundtrip(); err != nil {
		t.Fatal(err)
	}
	f.send(999, 0, wlUint(1)) // an event for an unknown object is skipped
	f.send(1, 0, wlUint(3), wlUint(2), wlString("bad request"))
	<-c.Done()
	if err := c.Err(); err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("Err = %v", err)
	}
	if err := c.request(1, 0, wlUint(9)); err == nil {
		t.Fatal("request on a failed connection succeeded")
	}
}

func TestWlConnPassesFileDescriptors(t *testing.T) {
	f := newFakeWl(t)
	f.addGlobal(1, "fd_sink", 1)
	got := make(chan int, 1)
	f.handle("fd_sink", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		if v := m.Uint(); len(fds) != 1 || v != 42 {
			t.Errorf("request carried %d fds and argument %d", len(fds), v)
			return
		}
		got <- fds[0]
	})
	c := f.connect()
	reg, err := c.getRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := reg.bind(reg.find("fd_sink")[0], 1, nil)
	var p [2]int
	if err := unix.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	if err := c.request(id, 0, wlFD(p[1]), wlUint(42)); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(p[1])
	fd := <-got
	if _, err := unix.Write(fd, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	buf := make([]byte, 2)
	if n, _ := unix.Read(p[0], buf); string(buf[:n]) != "ok" {
		t.Fatalf("read %q through the passed descriptor", buf[:n])
	}
}

func TestWlOutputsFollowsTheLayout(t *testing.T) {
	for _, xdg := range []uint32{0, 2, 3} {
		t.Run(map[uint32]string{0: "no-xdg", 2: "xdg-v2", 3: "xdg-v3"}[xdg], func(t *testing.T) {
			f := newFakeWl(t)
			s := f.serveOutputs(xdg)
			s.set(100, fakeWlOutput{name: "eDP-1", w: 1280, h: 800, modeW: 2560, modeH: 1600, scale: 2})
			c := f.connect()
			outs, _, err := watchWlOutputs(c)
			if err != nil {
				t.Fatal(err)
			}
			want := wlOutputInfo{Name: "eDP-1", Rect: displayRect{W: 1280, H: 800}, ModeW: 2560, ModeH: 1600, Scale: 2}
			if got := outs.Snapshot(); len(got) != 1 || got[0] != want {
				t.Fatalf("Snapshot = %+v, want %+v", got, want)
			}
			drain(outs.Changed())

			s.set(101, fakeWlOutput{name: "HEADLESS-1", x: 1280, w: 1920, h: 1080, modeW: 1920, modeH: 1080, scale: 1})
			select {
			case <-outs.Changed():
			case <-time.After(3 * time.Second):
				t.Fatal("no change notified for a new output")
			}
			waitFor(t, "the new output", func() bool { return len(outs.Snapshot()) == 2 })
			id, info, ok := outs.lookup("HEADLESS-1")
			if !ok || id == 0 || info.Rect != (displayRect{X: 1280, W: 1920, H: 1080}) {
				t.Fatalf("lookup = %d %+v %v", id, info, ok)
			}

			s.set(101, fakeWlOutput{name: "HEADLESS-1", x: 1280, w: 1600, h: 1200, modeW: 1600, modeH: 1200, scale: 1})
			waitFor(t, "the mode change", func() bool {
				_, info, _ := outs.lookup("HEADLESS-1")
				return info.ModeW == 1600 && info.Rect.H == 1200
			})

			s.remove(101)
			waitFor(t, "the removal", func() bool { return len(outs.Snapshot()) == 1 })
			if _, _, ok := outs.lookup("HEADLESS-1"); ok {
				t.Fatal("removed output still found")
			}
			if err := c.roundtrip(); err != nil || c.Err() != nil {
				t.Fatalf("connection after the removal: %v %v", err, c.Err())
			}
		})
	}
}

func drain(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
