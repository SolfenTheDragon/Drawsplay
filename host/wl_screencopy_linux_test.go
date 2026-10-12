//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// scPicture builds an XRGB8888 buffer whose pixels are labelled by name.
func scPicture(rows ...string) ([]byte, scBuffer) {
	h, w := len(rows), len(rows[0])
	b := scBuffer{format: wlShmXRGB8888, width: w, height: h, stride: w*4 + 8}
	src := make([]byte, b.stride*h)
	for y, row := range rows {
		for x := range w {
			src[y*b.stride+x*4] = row[x] // the blue byte carries the label
		}
	}
	return src, b
}

func scLabels(dst []byte, w, h int) []string {
	rows := make([]string, h)
	for y := range h {
		var sb strings.Builder
		for x := range w {
			sb.WriteByte(dst[(y*w+x)*4])
		}
		rows[y] = sb.String()
	}
	return rows
}

// The buffer holds the upright picture as the output's transform leaves it:
// flipped around the vertical axis (flipped transforms), then turned
// counter-clockwise.
func TestConvertScreencopyFrameUndoesTheOutputTransform(t *testing.T) {
	src, b := scPicture("abc", "def")
	for _, tc := range []struct {
		tr   int
		want []string
	}{
		{0, []string{"abc", "def"}},
		{1, []string{"da", "eb", "fc"}}, // 90: turn the buffer clockwise
		{2, []string{"fed", "cba"}},     // 180
		{3, []string{"cf", "be", "ad"}}, // 270
		{4, []string{"cba", "fed"}},     // flipped
		{5, []string{"ad", "be", "cf"}}, // flipped 90
		{6, []string{"def", "abc"}},     // flipped 180
		{7, []string{"fc", "eb", "da"}}, // flipped 270
	} {
		w, h := len(tc.want[0]), len(tc.want)
		dst := make([]byte, w*h*4)
		if err := convertScreencopyFrame(dst, w, h, src, b, false, tc.tr); err != nil {
			t.Fatalf("transform %d: %v", tc.tr, err)
		}
		if got := scLabels(dst, w, h); strings.Join(got, "/") != strings.Join(tc.want, "/") {
			t.Errorf("transform %d: got %v, want %v", tc.tr, got, tc.want)
		}
	}
}

func TestConvertScreencopyFrameCropsAndInverts(t *testing.T) {
	src, b := scPicture("abc", "def", "ghi")
	dst := make([]byte, 2*2*4)
	if err := convertScreencopyFrame(dst, 2, 2, src, b, false, 0); err != nil {
		t.Fatal(err)
	}
	if got := scLabels(dst, 2, 2); strings.Join(got, "/") != "ab/de" {
		t.Fatalf("even crop = %v", got)
	}
	if err := convertScreencopyFrame(dst, 2, 2, src, b, true, 0); err != nil {
		t.Fatal(err)
	}
	if got := scLabels(dst, 2, 2); strings.Join(got, "/") != "gh/de" {
		t.Fatalf("bottom-up rows = %v", got)
	}
	if err := convertScreencopyFrame(dst, 2, 2, src, b, true, 1); err != nil {
		t.Fatal(err)
	}
	// Rows stored bottom up hold "ghi/def/abc"; turned clockwise: "adg/beh/cfi".
	if got := scLabels(dst, 2, 2); strings.Join(got, "/") != "ad/be" {
		t.Fatalf("bottom-up rows turned = %v", got)
	}
	if err := convertScreencopyFrame(make([]byte, 4*4*4), 4, 4, src, b, false, 0); err == nil {
		t.Fatal("a frame larger than the picture was accepted")
	}
}

func TestConvertScreencopyFrameFormats(t *testing.T) {
	const r, g, bl = 0x12, 0x9a, 0xf0
	pixel := map[uint32][]byte{
		wlShmXRGB8888: {bl, g, r, 0xff},
		wlShmARGB8888: {bl, g, r, 0x80},
		wlShmXBGR8888: {r, g, bl, 0xff},
		wlShmABGR8888: {r, g, bl, 0x80},
	}
	ten := func(v byte) uint32 { return uint32(v)<<2 | 3 }
	for _, f := range []uint32{wlShmXRGB2101010, wlShmARGB2101010} {
		pixel[f] = binary.LittleEndian.AppendUint32(nil, 3<<30|ten(r)<<20|ten(g)<<10|ten(bl))
	}
	for _, f := range []uint32{wlShmXBGR2101010, wlShmABGR2101010} {
		pixel[f] = binary.LittleEndian.AppendUint32(nil, 3<<30|ten(bl)<<20|ten(g)<<10|ten(r))
	}
	for format, p := range pixel {
		for _, tr := range []int{0, 2} { // the row copy and the per-pixel path
			src := bytes.Repeat(p, 4)
			dst := make([]byte, 2*2*4)
			b := scBuffer{format: format, width: 2, height: 2, stride: 8}
			if err := convertScreencopyFrame(dst, 2, 2, src, b, false, tr); err != nil {
				t.Fatalf("format %#x: %v", format, err)
			}
			if got := dst[:3]; got[0] != bl || got[1] != g || got[2] != r {
				t.Errorf("format %#x transform %d: bgr = % x", format, tr, got)
			}
		}
	}
	if screencopyFormatSupported(0x3231564e) { // NV12
		t.Fatal("NV12 accepted")
	}
}

// fakeScreencopy serves wl_shm and wlr-screencopy on a fake compositor: every
// copy fills the buffer with the frame's number, unless hold is set.
type fakeScreencopy struct {
	mu        sync.Mutex
	layout    scBuffer
	mem       map[uint32][]byte // wl_buffer id -> memory
	pools     map[uint32][]byte
	frames    int
	overlay   []int32
	outputs   []uint32
	hold      bool // leave copies unanswered
	fail      bool // answer copies with failed
	yInvert   bool
	captures  int // capture_output requests
	failures  int
	destroyed int // frames destroyed by the client
}

func (f *fakeWl) serveScreencopy(layout scBuffer) *fakeScreencopy {
	s := &fakeScreencopy{layout: layout, mem: map[uint32][]byte{}, pools: map[uint32][]byte{}}
	f.addGlobal(80, "wl_shm", 1)
	f.addGlobal(81, "zwlr_screencopy_manager_v1", 3)
	f.handle("wl_shm", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		pool, size := m.Uint(), int(m.Int())
		f.newObject(pool, "wl_shm_pool")
		if len(fds) != 1 {
			f.t.Errorf("create_pool carried %d fds", len(fds))
			return
		}
		mem, err := unix.Mmap(fds[0], 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
		_ = unix.Close(fds[0])
		if err != nil {
			f.t.Errorf("mmap pool: %v", err)
			return
		}
		s.mu.Lock()
		s.pools[pool] = mem
		s.mu.Unlock()
	})
	f.handle("wl_shm_pool", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		switch op {
		case 0: // create_buffer
			buffer, offset, w, h, stride, format := m.Uint(), m.Int(), m.Int(), m.Int(), m.Int(), m.Uint()
			f.newObject(buffer, "wl_buffer")
			s.mu.Lock()
			if offset != 0 || (scBuffer{format, int(w), int(h), int(stride)}) != s.layout {
				f.t.Errorf("buffer %dx%d stride %d format %#x does not match the offer", w, h, stride, format)
			}
			s.mem[buffer] = s.pools[id]
			s.mu.Unlock()
		case 1: // destroy
			f.deleteObject(id)
		}
	})
	f.handle("wl_buffer", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		f.deleteObject(id)
	})
	f.handle("zwlr_screencopy_manager_v1", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		if op == 2 { // destroy
			f.deleteObject(id)
			return
		}
		frame, overlay, output := m.Uint(), m.Int(), m.Uint()
		f.newObject(frame, "zwlr_screencopy_frame_v1")
		s.mu.Lock()
		s.captures++
		s.overlay = append(s.overlay, overlay)
		s.outputs = append(s.outputs, output)
		l := s.layout
		s.mu.Unlock()
		f.send(frame, scEventBuffer, wlUint(l.format), wlUint(uint32(l.width)), wlUint(uint32(l.height)), wlUint(uint32(l.stride)))
		f.send(frame, 5, wlUint(0x34325258), wlUint(uint32(l.width)), wlUint(uint32(l.height))) // linux_dmabuf
		f.send(frame, scEventBufferDone)
	})
	f.handle("zwlr_screencopy_frame_v1", func(f *fakeWl, id uint32, op uint16, m *wlMessage, fds []int) {
		if op == 1 { // destroy
			s.mu.Lock()
			s.destroyed++
			s.mu.Unlock()
			f.deleteObject(id)
			return
		}
		buffer := m.Uint()
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case s.hold:
			return
		case s.fail:
			s.failures++
			f.send(id, scEventFailed)
			return
		}
		s.frames++
		mem := s.mem[buffer]
		for i := range mem {
			mem[i] = byte(s.frames)
		}
		flags := uint32(0)
		if s.yInvert {
			flags = screencopyFlagYInvert
		}
		f.send(id, scEventFlags, wlUint(flags))
		f.send(id, 4, wlUint(0), wlUint(0), wlUint(1), wlUint(1)) // damage
		f.send(id, scEventReady, wlUint(0), wlUint(1), wlUint(2))
	})
	return s
}

func (s *fakeScreencopy) set(fn func(s *fakeScreencopy)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *fakeScreencopy) get(fn func(s *fakeScreencopy) int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s)
}

// frameSink collects the frames a pump writes, dropping those nobody reads
// in time.
type frameSink struct {
	frames chan []byte
}

func (w *frameSink) Write(p []byte) (int, error) {
	select {
	case w.frames <- append([]byte(nil), p...):
	default:
	}
	return len(p), nil
}

// nextNew returns the first frame that differs from old.
func (w *frameSink) nextNew(t *testing.T, old []byte) []byte {
	t.Helper()
	for {
		if f := w.next(t); !bytes.Equal(f, old) {
			return f
		}
	}
}

func (w *frameSink) next(t *testing.T) []byte {
	t.Helper()
	select {
	case f := <-w.frames:
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("no frame written")
		return nil
	}
}

type scHarness struct {
	f    *fakeWl
	outs *fakeWlOutputs
	sc   *fakeScreencopy
	sink *frameSink
	done chan error
}

func startScreencopyPump(t *testing.T, layout scBuffer, transform int32, w, h, fps int, setup ...func(*fakeScreencopy)) *scHarness {
	t.Helper()
	f := newFakeWl(t)
	outs := f.serveOutputs(3)
	outs.set(100, fakeWlOutput{name: "eDP-1", w: 1280, h: 800, modeW: 1280, modeH: 800, scale: 1})
	outs.set(101, fakeWlOutput{name: "HEADLESS-1", x: 1280, w: int32(w), h: int32(h), modeW: int32(layout.width), modeH: int32(layout.height), scale: 1, trans: transform})
	sc := f.serveScreencopy(layout)
	for _, fn := range setup {
		sc.set(fn)
	}
	c := f.connect()
	ctx, cancel := context.WithCancel(context.Background())
	hs := &scHarness{f: f, outs: outs, sc: sc, sink: &frameSink{frames: make(chan []byte, 256)}, done: make(chan error, 1)}
	go func() { hs.done <- pumpScreencopy(ctx, c, hs.sink, "HEADLESS-1", w, h, fps) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-hs.done:
		case <-time.After(3 * time.Second):
			t.Error("pump did not stop")
		}
	})
	return hs
}

func (hs *scHarness) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-hs.done:
		hs.done <- err // for the cleanup
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("pump did not end")
		return nil
	}
}

func TestPumpScreencopyStreamsTheOutput(t *testing.T) {
	layout := scBuffer{format: wlShmXRGB8888, width: 5, height: 3, stride: 24}
	hs := startScreencopyPump(t, layout, 0, 4, 2, 100)
	first := hs.sink.next(t)
	if len(first) != 4*2*4 {
		t.Fatalf("frame of %d bytes, want %d", len(first), 4*2*4)
	}
	second := hs.sink.next(t)
	if first[0] == 0 || second[0] != first[0]+1 || !bytes.Equal(second, bytes.Repeat(second[:1], len(second))) {
		t.Fatalf("frames %d then %d", first[0], second[0])
	}
	waitFor(t, "frames destroyed", func() bool {
		var n int
		hs.sc.set(func(s *fakeScreencopy) { n = s.destroyed })
		return n >= 2
	})
	hs.sc.set(func(s *fakeScreencopy) {
		for _, o := range s.overlay {
			if o != 1 {
				t.Errorf("captured without the pointer (overlay_cursor %d)", o)
			}
		}
		for _, id := range s.outputs {
			if hs.f.iface(id) != "wl_output" {
				t.Errorf("captured object %d, not a wl_output", id)
			}
		}
	})
}

// Like x11grab, the stream goes on while the compositor draws nothing, and
// picks up again once it draws.
func TestPumpScreencopyRepeatsTheLastFrameWhileStalled(t *testing.T) {
	layout := scBuffer{format: wlShmXRGB8888, width: 2, height: 2, stride: 8}
	hs := startScreencopyPump(t, layout, 0, 2, 2, 30)
	hs.sink.next(t)
	hs.sc.set(func(s *fakeScreencopy) { s.hold = true })
	captures := hs.sc.get(func(s *fakeScreencopy) int { return s.captures })

	// Copies under way when the hold began may still come in; then the
	// last frame repeats.
	var last []byte
	var lastAt time.Time
	for repeats := 0; repeats < 3; {
		f := hs.sink.next(t)
		now := time.Now()
		if bytes.Equal(f, last) {
			if gap := now.Sub(lastAt); gap < screencopyStallInterval/2 {
				t.Fatalf("repeated after %s", gap)
			}
			repeats++
		} else {
			repeats = 0
		}
		last, lastAt = f, now
	}

	// The unanswered frame is asked for again.
	waitFor(t, "a new capture", func() bool {
		return hs.sc.get(func(s *fakeScreencopy) int { return s.captures }) > captures+1
	})
	hs.sc.set(func(s *fakeScreencopy) { s.hold = false })
	if f := hs.sink.nextNew(t, last); f[0] == last[0] {
		t.Fatalf("frame %d after the stall", f[0])
	}
}

func TestPumpScreencopyRetriesFailedCopies(t *testing.T) {
	layout := scBuffer{format: wlShmXRGB8888, width: 2, height: 2, stride: 8}
	hs := startScreencopyPump(t, layout, 0, 2, 2, 100, func(s *fakeScreencopy) { s.fail = true })
	if f := hs.sink.next(t); f[0] != 0 {
		t.Fatalf("frame %d before any copy succeeded, want black", f[0])
	}
	failures := func() int { return hs.sc.get(func(s *fakeScreencopy) int { return s.failures }) }
	waitFor(t, "failed copies", func() bool { return failures() >= 2 })
	before := failures()
	time.Sleep(300 * time.Millisecond)
	if n := failures() - before; n > 300/int(screencopyFailedRetry/time.Millisecond)+2 {
		t.Fatalf("%d retries in 300ms", n)
	}
	hs.sc.set(func(s *fakeScreencopy) { s.fail = false })
	if f := hs.sink.nextNew(t, make([]byte, 2*2*4)); f[0] == 0 {
		t.Fatal("no frame after the failures")
	}
	select {
	case err := <-hs.done:
		t.Fatalf("pump ended: %v", err)
	default:
	}
}

func TestPumpScreencopyTurnsARotatedOutput(t *testing.T) {
	layout := scBuffer{format: wlShmXRGB8888, width: 4, height: 2, stride: 16}
	hs := startScreencopyPump(t, layout, wlOutputTransform90, 2, 4, 100)
	if f := hs.sink.next(t); len(f) != 2*4*4 {
		t.Fatalf("frame of %d bytes for a 2x4 stream", len(f))
	}
}

func TestPumpScreencopyEndsWhenTheOutputChanges(t *testing.T) {
	t.Run("resized", func(t *testing.T) {
		layout := scBuffer{format: wlShmXRGB8888, width: 4, height: 2, stride: 16}
		hs := startScreencopyPump(t, layout, 0, 2, 2, 100)
		if err := hs.wait(t); err == nil || !strings.Contains(err.Error(), "now 4x2") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("gone", func(t *testing.T) {
		layout := scBuffer{format: wlShmXRGB8888, width: 2, height: 2, stride: 8}
		hs := startScreencopyPump(t, layout, 0, 2, 2, 100)
		hs.sink.next(t)
		hs.outs.remove(101)
		if err := hs.wait(t); err == nil || !strings.Contains(err.Error(), "gone") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("format", func(t *testing.T) {
		layout := scBuffer{format: 0x3231564e, width: 2, height: 2, stride: 8}
		hs := startScreencopyPump(t, layout, 0, 2, 2, 100)
		if err := hs.wait(t); err == nil || !strings.Contains(err.Error(), "pixel format") {
			t.Fatalf("err = %v", err)
		}
	})
}
