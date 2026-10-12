//go:build linux

package main

// Capture of a wlroots (sway) or Hyprland output through wlr-screencopy, the
// x11grab of those desktops: every frame period the host asks the compositor
// for a copy of the output in shared memory and writes it, upright and as
// packed bgr0, to an ffmpeg rawvideo input (pipelineSpec.Input).
//
// Like x11grab it records at a steady rate whatever the desktop shows, the
// pointer included, and keeps feeding the encoder when the desktop stands
// still; a copy request makes the compositor draw a frame even when nothing
// changed.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	screencopyManagerVersion = 3
	wlShmVersion             = 1
	// screencopyStallInterval: while the compositor sends no frame (an
	// output that is switched off draws none) the last one is repeated
	// this often, so the stream goes on as x11grab's would.
	screencopyStallInterval = 250 * time.Millisecond
	// screencopyFailedRetry bounds how fast a failed copy is retried.
	screencopyFailedRetry = 50 * time.Millisecond
	// screencopyFrameTimeout: a frame the compositor leaves unanswered this
	// long is given up and asked for again. Hyprland answers none while the
	// output's display is switched off (DPMS).
	screencopyFrameTimeout = 2 * time.Second
	// screencopyPipeSize is the pipe buffer requested for the frames, so a
	// frame takes few writes (the kernel caps it, 1 MiB by default).
	screencopyPipeSize = 1 << 20
)

// wl_shm formats a compositor may offer for screencopy.
const (
	wlShmARGB8888    = 0
	wlShmXRGB8888    = 1
	wlShmXBGR8888    = 0x34324258
	wlShmABGR8888    = 0x34324241
	wlShmXRGB2101010 = 0x30335258
	wlShmARGB2101010 = 0x30335241
	wlShmXBGR2101010 = 0x30334258
	wlShmABGR2101010 = 0x30334241
)

const screencopyFlagYInvert = 1

// screencopyInput is the pipelineSpec.Input of a captureScreencopy target.
func screencopyInput(target captureTarget, fps int) func(context.Context, io.Writer) error {
	return func(ctx context.Context, w io.Writer) error {
		c, err := dialWayland()
		if err != nil {
			return err
		}
		defer c.Close()
		growPipe(w, screencopyPipeSize)
		return pumpScreencopy(ctx, c, w, target.OutputName, target.Width, target.Height, fps)
	}
}

// growPipe asks for a larger buffer on w when it is a pipe.
func growPipe(w io.Writer, size int) {
	sc, ok := w.(syscall.Conn)
	if !ok {
		return
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_, _ = unix.FcntlInt(fd, unix.F_SETPIPE_SZ, size)
	})
}

// scBuffer is the layout of a screencopy buffer.
type scBuffer struct {
	format                uint32
	width, height, stride int
}

// scShm is the shared-memory wl_buffer the compositor copies frames into.
type scShm struct {
	scBuffer
	id  uint32
	mem []byte
}

// scFrameEvent is one zwlr_screencopy_frame_v1 event.
type scFrameEvent struct {
	op   uint16
	buf  scBuffer // buffer
	flag uint32   // flags
}

const (
	scEventBuffer     = 0
	scEventFlags      = 1
	scEventReady      = 2
	scEventFailed     = 3
	scEventBufferDone = 6
)

// scFrame is a frame being captured.
type scFrame struct {
	id        uint32
	events    chan scFrameEvent
	transform int
	buf       scBuffer
	haveBuf   bool
	copied    bool
	yInvert   bool
}

type screencopy struct {
	c          *wlConn
	outs       *wlOutputs
	mgr, mgrV  uint32
	shm        uint32
	outputName string
	// width x height is the stream size: the output's upright picture,
	// cropped to even dimensions.
	width, height int
	pool          *scShm
}

// pumpScreencopy writes frames of the output called outputName to w at fps
// until ctx ends (nil) or the output can no longer be recorded: it is gone,
// or its picture no longer has the stream's size.
func pumpScreencopy(ctx context.Context, c *wlConn, w io.Writer, outputName string, width, height, fps int) error {
	if width <= 0 || height <= 0 || fps <= 0 {
		return fmt.Errorf("invalid screencopy stream %dx%d at %d fps", width, height, fps)
	}
	outs, reg, err := watchWlOutputs(c)
	if err != nil {
		return err
	}
	mgrs, shms := reg.find("zwlr_screencopy_manager_v1"), reg.find("wl_shm")
	if len(mgrs) == 0 {
		return errors.New("the compositor offers no wlr-screencopy")
	}
	if len(shms) == 0 {
		return errors.New("the compositor offers no wl_shm")
	}
	s := &screencopy{c: c, outs: outs, outputName: outputName, width: width, height: height}
	if s.mgr, s.mgrV, err = reg.bind(mgrs[0], screencopyManagerVersion, nil); err != nil {
		return err
	}
	if s.shm, _, err = reg.bind(shms[0], wlShmVersion, nil); err != nil {
		return err
	}
	defer s.release()

	out := make([]byte, width*height*4) // black until the first frame
	period := time.Second / time.Duration(fps)
	request := time.NewTimer(0)
	defer request.Stop()
	stall := time.NewTimer(screencopyStallInterval)
	defer stall.Stop()
	var frame *scFrame
	var lastRequest time.Time
	dropFrame := func() {
		_ = c.destroy(frame.id, 1)
		frame = nil
	}
	defer func() {
		if frame != nil {
			dropFrame()
		}
	}()
	write := func() error {
		if _, err := w.Write(out); err != nil {
			return err
		}
		stall.Reset(screencopyStallInterval)
		return nil
	}
	for {
		var events chan scFrameEvent
		if frame != nil {
			events = frame.events
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.Done():
			return c.Err()
		case <-outs.Changed():
			if _, _, ok := outs.lookup(outputName); !ok {
				return fmt.Errorf("output %s is gone", outputName)
			}
		case <-request.C:
			if frame != nil {
				continue
			}
			lastRequest = time.Now()
			if frame, err = s.capture(); err != nil {
				return err
			}
		case ev := <-events:
			done, ok, err := s.frameEvent(frame, ev)
			if err != nil {
				return err
			}
			if !done {
				continue
			}
			transform, yInvert, buf := frame.transform, frame.yInvert, frame.buf
			dropFrame()
			if !ok {
				request.Reset(max(period, screencopyFailedRetry))
				continue
			}
			if err := convertScreencopyFrame(out, width, height, s.pool.mem, buf, yInvert, transform); err != nil {
				return err
			}
			if err := write(); err != nil {
				return err
			}
			request.Reset(time.Until(lastRequest.Add(period)))
		case <-stall.C:
			if frame != nil && time.Since(lastRequest) >= screencopyFrameTimeout {
				dropFrame()
				request.Reset(0)
			}
			if err := write(); err != nil {
				return err
			}
		}
	}
}

// capture asks for the output's next frame.
func (s *screencopy) capture() (*scFrame, error) {
	output, info, ok := s.outs.lookup(s.outputName)
	if !ok {
		return nil, fmt.Errorf("output %s is gone", s.outputName)
	}
	f := &scFrame{events: make(chan scFrameEvent, 16), transform: info.Transform}
	id, err := s.c.create(func(op uint16, m *wlMessage) {
		ev := scFrameEvent{op: op}
		switch op {
		case scEventBuffer:
			ev.buf = scBuffer{format: m.Uint(), width: int(m.Uint()), height: int(m.Uint()), stride: int(m.Uint())}
		case scEventFlags:
			ev.flag = m.Uint()
		case scEventReady, scEventFailed, scEventBufferDone:
		default:
			return // damage, linux_dmabuf
		}
		select {
		case f.events <- ev:
		default:
		}
	}, s.mgr, 0, func(id uint32) []wlArg {
		return []wlArg{wlUint(id), wlInt(1), wlUint(output)} // capture_output, with the pointer
	})
	f.id = id
	return f, err
}

// frameEvent handles an event of frame f: done once the frame is ready (ok)
// or failed.
func (s *screencopy) frameEvent(f *scFrame, ev scFrameEvent) (done, ok bool, err error) {
	switch ev.op {
	case scEventBuffer:
		f.buf, f.haveBuf = ev.buf, true
		if s.mgrV < 3 { // no buffer_done: this was the only buffer type
			return false, false, s.copy(f)
		}
	case scEventBufferDone:
		if !f.haveBuf {
			return false, false, errors.New("the compositor offers no shared-memory screencopy buffer")
		}
		return false, false, s.copy(f)
	case scEventFlags:
		f.yInvert = ev.flag&screencopyFlagYInvert != 0
	case scEventReady:
		return true, f.copied, nil
	case scEventFailed:
		return true, false, nil
	}
	return false, false, nil
}

// copy has the compositor copy frame f into the shared-memory buffer.
func (s *screencopy) copy(f *scFrame) error {
	if f.copied {
		return nil
	}
	b := f.buf
	if !screencopyFormatSupported(b.format) {
		return fmt.Errorf("unsupported screencopy pixel format %#x", b.format)
	}
	if b.width <= 0 || b.height <= 0 || b.stride < b.width*4 {
		return fmt.Errorf("invalid screencopy buffer %dx%d (stride %d)", b.width, b.height, b.stride)
	}
	uw, uh := b.width, b.height
	if wlTransformSwapsAxes(f.transform) {
		uw, uh = uh, uw
	}
	if uw&^1 != s.width || uh&^1 != s.height {
		return fmt.Errorf("output %s is now %dx%d, not %dx%d", s.outputName, uw, uh, s.width, s.height)
	}
	if s.pool == nil || s.pool.scBuffer != b {
		if err := s.newBuffer(b); err != nil {
			return err
		}
	}
	f.copied = true
	return s.c.request(f.id, 0, wlUint(s.pool.id))
}

// newBuffer replaces the shared-memory buffer with one of layout b.
func (s *screencopy) newBuffer(b scBuffer) error {
	s.releaseBuffer()
	size := b.stride * b.height
	fd, err := unix.MemfdCreate("drawsplay-screencopy", unix.MFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("screencopy buffer: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		return fmt.Errorf("screencopy buffer: %w", err)
	}
	mem, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("screencopy buffer: %w", err)
	}
	pool, err := s.c.create(nil, s.shm, 0, func(id uint32) []wlArg { // wl_shm.create_pool
		return []wlArg{wlUint(id), wlFD(fd), wlInt(int32(size))}
	})
	if err != nil {
		_ = unix.Munmap(mem)
		return err
	}
	buffer, err := s.c.create(nil, pool, 0, func(id uint32) []wlArg { // wl_shm_pool.create_buffer
		return []wlArg{wlUint(id), wlInt(0), wlInt(int32(b.width)), wlInt(int32(b.height)), wlInt(int32(b.stride)), wlUint(b.format)}
	})
	_ = s.c.destroy(pool, 1) // the buffer keeps the memory
	if err != nil {
		_ = unix.Munmap(mem)
		return err
	}
	s.pool = &scShm{scBuffer: b, id: buffer, mem: mem}
	return nil
}

func (s *screencopy) releaseBuffer() {
	if s.pool == nil {
		return
	}
	_ = s.c.destroy(s.pool.id, 0)
	_ = unix.Munmap(s.pool.mem)
	s.pool = nil
}

func (s *screencopy) release() {
	s.releaseBuffer()
	_ = s.c.destroy(s.mgr, 2)
}

func screencopyFormatSupported(format uint32) bool {
	switch format {
	case wlShmARGB8888, wlShmXRGB8888, wlShmABGR8888, wlShmXBGR8888,
		wlShmARGB2101010, wlShmXRGB2101010, wlShmABGR2101010, wlShmXBGR2101010:
		return true
	}
	return false
}

// wlTransformInvert is the transform that undoes tr (wlroots'
// wlr_output_transform_invert).
func wlTransformInvert(tr int) int {
	if tr&1 != 0 && tr&4 == 0 {
		return tr ^ 2
	}
	return tr
}

// wlTransformPoint is where transform tr puts pixel x,y of a width x height
// picture (wlroots' wlr_box_transform, for a one-pixel box).
func wlTransformPoint(x, y, tr, width, height int) (int, int) {
	switch tr & 7 {
	case 1: // 90
		return height - y - 1, x
	case 2: // 180
		return width - x - 1, height - y - 1
	case 3: // 270
		return y, width - x - 1
	case 4: // flipped
		return width - x - 1, y
	case 5: // flipped 90
		return y, x
	case 6: // flipped 180
		return x, height - y - 1
	case 7: // flipped 270
		return height - y - 1, width - x - 1
	}
	return x, y
}

// convertScreencopyFrame writes the top-left w x h pixels of the upright
// picture in src to dst, as packed bgr0.
//
// src is a buffer of an output with transform tr, as screencopy delivers it:
// as the output's buffer holds it, which a compositor draws the upright
// (logical) picture into through the inverse of tr. With yInvert its rows
// are stored bottom up.
func convertScreencopyFrame(dst []byte, w, h int, src []byte, b scBuffer, yInvert bool, tr int) error {
	uw, uh := b.width, b.height
	if wlTransformSwapsAxes(tr) {
		uw, uh = uh, uw
	}
	if w <= 0 || h <= 0 || w > uw || h > uh || len(dst) < w*h*4 {
		return fmt.Errorf("cannot fit a %dx%d frame from a %dx%d picture", w, h, uw, uh)
	}
	if b.stride < b.width*4 || len(src) < b.stride*b.height {
		return errors.New("screencopy buffer smaller than its layout")
	}
	inv := wlTransformInvert(tr)
	offset := func(x, y int) int {
		bx, by := wlTransformPoint(x, y, inv, uw, uh)
		if yInvert {
			by = b.height - 1 - by
		}
		return by*b.stride + bx*4
	}
	origin := offset(0, 0)
	dx := offset(1, 0) - origin
	dy := offset(0, 1) - origin
	for y := 0; y < h; y++ {
		row := dst[y*w*4 : (y+1)*w*4]
		o := origin + y*dy
		switch b.format {
		case wlShmARGB8888, wlShmXRGB8888: // B, G, R, X in memory, as bgr0
			if dx == 4 {
				copy(row, src[o:o+w*4])
				continue
			}
			for x := 0; x < len(row); x += 4 {
				copy(row[x:x+4], src[o:o+4])
				o += dx
			}
		case wlShmABGR8888, wlShmXBGR8888: // R, G, B, X
			for x := 0; x < len(row); x += 4 {
				p := src[o : o+4]
				row[x], row[x+1], row[x+2], row[x+3] = p[2], p[1], p[0], 0
				o += dx
			}
		case wlShmARGB2101010, wlShmXRGB2101010:
			for x := 0; x < len(row); x += 4 {
				v := binary.LittleEndian.Uint32(src[o:])
				row[x], row[x+1], row[x+2], row[x+3] = byte(v>>2), byte(v>>12), byte(v>>22), 0
				o += dx
			}
		case wlShmABGR2101010, wlShmXBGR2101010:
			for x := 0; x < len(row); x += 4 {
				v := binary.LittleEndian.Uint32(src[o:])
				row[x], row[x+1], row[x+2], row[x+3] = byte(v>>22), byte(v>>12), byte(v>>2), 0
				o += dx
			}
		default:
			return fmt.Errorf("unsupported screencopy pixel format %#x", b.format)
		}
	}
	return nil
}
