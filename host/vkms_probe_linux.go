//go:build linux

package main

// vkms scanout probe.
//
// Why: under X11 the modesetting driver adds vkms as a PRIME output sink.
// Its frames are buffers the real GPU allocates and exports as dma-bufs;
// vkms imports them and, in vkms_prepare_fb, maps them into the kernel with
// dma_buf_vmap. Some exporters cannot be mapped: Intel's xe driver has no
// GEM .vmap, so drm_gem_dmabuf_vmap returns -EOPNOTSUPP for every xe buffer
// (dma_buf_vmap also WARNs once). The modeset then fails, and with X and the
// desktop already driving the new output this has frozen the display. The
// desktop (Cinnamon's display settings daemon, for one) enables a new output
// by itself the moment X adds the GPU, so not calling xrandr is no
// protection: X must never see a vkms device the GPU cannot feed.
//
// How: a udev rule (72-drawsplay-vkms-hold.rules) gives a freshly loaded
// vkms device a seat name X ignores, until /run/drawsplay/vkms-verified
// exists. While vkms is held, the host is its only user and therefore its
// DRM master, and repeats exactly what X would do: allocate a buffer on each
// real GPU, export it, import it into vkms, add a framebuffer, set a mode
// and page-flip. Only when every GPU passes does the display helper release
// vkms to X. A failure costs nothing: X never knew the device, so it is
// unloaded again and the spare-connector fallback is used.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// DRM ioctl numbers (include/uapi/drm/drm.h): _IOC(dir, 'd', nr, size).
func drmIOC(dir, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | uintptr('d')<<8 | nr
}

const (
	drmIocWrite     = 1
	drmIocReadWrite = 3
)

type drmModeCardRes struct {
	FbIDPtr, CrtcIDPtr, ConnectorIDPtr, EncoderIDPtr     uint64
	CountFbs, CountCrtcs, CountConnectors, CountEncoders uint32
	MinWidth, MaxWidth, MinHeight, MaxHeight             uint32
}

type drmModeInfo struct {
	Clock                                         uint32
	Hdisplay, HsyncStart, HsyncEnd, Htotal, Hskew uint16
	Vdisplay, VsyncStart, VsyncEnd, Vtotal, Vscan uint16
	Vrefresh, Flags, Type                         uint32
	Name                                          [32]byte
}

type drmModeGetConnector struct {
	EncodersPtr, ModesPtr, PropsPtr, PropValuesPtr uint64
	CountModes, CountProps, CountEncoders          uint32
	EncoderID, ConnectorID, ConnectorType          uint32
	ConnectorTypeID, Connection, MmWidth, MmHeight uint32
	Subpixel, Pad                                  uint32
}

type drmModeGetEncoder struct {
	EncoderID, EncoderType, CrtcID, PossibleCrtcs, PossibleClones uint32
}

type drmModeCreateDumb struct {
	Height, Width, Bpp, Flags, Handle, Pitch uint32
	Size                                     uint64
}

type drmPrimeHandle struct {
	Handle, Flags uint32
	Fd            int32
}

type drmGemClose struct{ Handle, Pad uint32 }

type drmModeFbCmd2 struct {
	FbID, Width, Height, PixelFormat, Flags uint32
	Handles, Pitches, Offsets               [4]uint32
	_                                       uint32
	Modifier                                [4]uint64
}

type drmModeCrtc struct {
	SetConnectorsPtr                    uint64
	CountConnectors, CrtcID, FbID, X, Y uint32
	GammaSize, ModeValid                uint32
	Mode                                drmModeInfo
}

type drmModeCrtcPageFlip struct {
	CrtcID, FbID, Flags, Reserved uint32
	UserData                      uint64
}

var (
	drmIoctlGemClose        = drmIOC(drmIocWrite, 0x09, unsafe.Sizeof(drmGemClose{}))
	drmIoctlPrimeHandleToFD = drmIOC(drmIocReadWrite, 0x2d, unsafe.Sizeof(drmPrimeHandle{}))
	drmIoctlPrimeFDToHandle = drmIOC(drmIocReadWrite, 0x2e, unsafe.Sizeof(drmPrimeHandle{}))
	drmIoctlGetResources    = drmIOC(drmIocReadWrite, 0xa0, unsafe.Sizeof(drmModeCardRes{}))
	drmIoctlSetCrtc         = drmIOC(drmIocReadWrite, 0xa2, unsafe.Sizeof(drmModeCrtc{}))
	drmIoctlGetEncoder      = drmIOC(drmIocReadWrite, 0xa6, unsafe.Sizeof(drmModeGetEncoder{}))
	drmIoctlGetConnector    = drmIOC(drmIocReadWrite, 0xa7, unsafe.Sizeof(drmModeGetConnector{}))
	drmIoctlRmFB            = drmIOC(drmIocReadWrite, 0xaf, 4)
	drmIoctlPageFlip        = drmIOC(drmIocReadWrite, 0xb0, unsafe.Sizeof(drmModeCrtcPageFlip{}))
	drmIoctlCreateDumb      = drmIOC(drmIocReadWrite, 0xb2, unsafe.Sizeof(drmModeCreateDumb{}))
	drmIoctlDestroyDumb     = drmIOC(drmIocReadWrite, 0xb4, 4)
	drmIoctlAddFB2          = drmIOC(drmIocReadWrite, 0xb8, unsafe.Sizeof(drmModeFbCmd2{}))
)

const (
	drmFormatXRGB8888     = 0x34325258 // 'XR24'
	drmModeConnected      = 1
	drmModeTypePreferred  = 1 << 3
	drmModePageFlipEvent  = 0x01
	drmEventFlipComplete  = 0x02
	drmCloexecRDWR        = unix.O_CLOEXEC | unix.O_RDWR
	vkmsProbeFlipTimeout  = 2 * time.Second
	vkmsProbeMaxDimension = 4096
)

// errVkmsProbeNotMaster: something else (normally the X server) already
// drives the vkms device, so the probe cannot set a mode. It says nothing
// about the kernel; the device was not held.
var errVkmsProbeNotMaster = errors.New("another program already drives the vkms device, so it could not be checked")

// vkmsScanoutError: the kernel cannot show a real GPU's buffers on vkms.
// This is the failure that freezes an X11 session; vkms must not be used.
type vkmsScanoutError struct {
	GPU  string // /dev/dri/cardN of the exporting GPU
	Step string
	Err  error
}

func (e *vkmsScanoutError) Error() string {
	return fmt.Sprintf("vkms cannot show buffers from %s (%s: %v)", e.GPU, e.Step, e.Err)
}

func (e *vkmsScanoutError) Unwrap() error { return e.Err }

func drmIoctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		switch errno {
		case 0:
			return nil
		case unix.EINTR, unix.EAGAIN:
			continue
		}
		return errno
	}
}

// pinnedArena hands out memory the Go runtime never moves, for the arrays
// whose addresses DRM ioctl structs carry as plain integers.
type pinnedArena struct {
	mem []byte
	off int
}

func newPinnedArena(size int) (*pinnedArena, error) {
	mem, err := unix.Mmap(-1, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	if err != nil {
		return nil, err
	}
	return &pinnedArena{mem: mem}, nil
}

func (a *pinnedArena) alloc(n int) (unsafe.Pointer, error) {
	n = (n + 7) &^ 7
	if n == 0 {
		n = 8
	}
	if a.off+n > len(a.mem) {
		return nil, errors.New("probe arena exhausted")
	}
	p := unsafe.Pointer(&a.mem[a.off])
	a.off += n
	return p, nil
}

func (a *pinnedArena) free() { _ = unix.Munmap(a.mem) }

// pinned copies v into the arena and returns a pointer to the copy.
func pinned[T any](a *pinnedArena, v T) (*T, error) {
	p, err := a.alloc(int(unsafe.Sizeof(v)))
	if err != nil {
		return nil, err
	}
	t := (*T)(p)
	*t = v
	return t, nil
}

func pinnedSlice[T any](a *pinnedArena, n int) ([]T, error) {
	var zero T
	p, err := a.alloc(n * int(unsafe.Sizeof(zero)))
	if err != nil {
		return nil, err
	}
	return unsafe.Slice((*T)(p), n), nil
}

func sliceAddr[T any](s []T) uint64 {
	if len(s) == 0 {
		return 0
	}
	return uint64(uintptr(unsafe.Pointer(&s[0])))
}

// vkmsProbeTarget is where the probe shows its test frame on vkms.
type vkmsProbeTarget struct {
	crtc, connector uint32
	mode            drmModeInfo
}

func vkmsFindTarget(fd int, a *pinnedArena) (vkmsProbeTarget, error) {
	var t vkmsProbeTarget
	res, err := pinned(a, drmModeCardRes{})
	if err != nil {
		return t, err
	}
	if err := drmIoctl(fd, drmIoctlGetResources, unsafe.Pointer(res)); err != nil {
		return t, fmt.Errorf("reading vkms resources: %w", err)
	}
	if res.CountCrtcs == 0 || res.CountConnectors == 0 || res.CountCrtcs > 64 || res.CountConnectors > 64 {
		return t, fmt.Errorf("vkms reports %d CRTCs and %d connectors", res.CountCrtcs, res.CountConnectors)
	}
	crtcs, err := pinnedSlice[uint32](a, int(res.CountCrtcs))
	if err != nil {
		return t, err
	}
	conns, err := pinnedSlice[uint32](a, int(res.CountConnectors))
	if err != nil {
		return t, err
	}
	*res = drmModeCardRes{
		CrtcIDPtr: sliceAddr(crtcs), CountCrtcs: uint32(len(crtcs)),
		ConnectorIDPtr: sliceAddr(conns), CountConnectors: uint32(len(conns)),
	}
	if err := drmIoctl(fd, drmIoctlGetResources, unsafe.Pointer(res)); err != nil {
		return t, fmt.Errorf("reading vkms resources: %w", err)
	}
	for _, id := range conns[:min(len(conns), int(res.CountConnectors))] {
		c, err := pinned(a, drmModeGetConnector{ConnectorID: id})
		if err != nil {
			return t, err
		}
		if err := drmIoctl(fd, drmIoctlGetConnector, unsafe.Pointer(c)); err != nil {
			continue
		}
		if c.Connection != drmModeConnected || c.CountModes == 0 || c.CountEncoders == 0 || c.CountModes > 256 || c.CountEncoders > 32 {
			continue
		}
		modes, err := pinnedSlice[drmModeInfo](a, int(c.CountModes))
		if err != nil {
			return t, err
		}
		encs, err := pinnedSlice[uint32](a, int(c.CountEncoders))
		if err != nil {
			return t, err
		}
		*c = drmModeGetConnector{
			ConnectorID: id,
			ModesPtr:    sliceAddr(modes), CountModes: uint32(len(modes)),
			EncodersPtr: sliceAddr(encs), CountEncoders: uint32(len(encs)),
		}
		if err := drmIoctl(fd, drmIoctlGetConnector, unsafe.Pointer(c)); err != nil {
			continue
		}
		modes = modes[:min(len(modes), int(c.CountModes))]
		encs = encs[:min(len(encs), int(c.CountEncoders))]
		mode, ok := pickProbeMode(modes)
		if !ok {
			continue
		}
		for _, encID := range encs {
			e, err := pinned(a, drmModeGetEncoder{EncoderID: encID})
			if err != nil {
				return t, err
			}
			if drmIoctl(fd, drmIoctlGetEncoder, unsafe.Pointer(e)) != nil {
				continue
			}
			for i, crtc := range crtcs[:min(len(crtcs), int(res.CountCrtcs))] {
				if e.PossibleCrtcs&(1<<uint(i)) != 0 {
					return vkmsProbeTarget{crtc: crtc, connector: id, mode: mode}, nil
				}
			}
		}
	}
	return t, errors.New("vkms has no connected output to test")
}

// pickProbeMode prefers the connector's preferred mode, within limits a dumb
// buffer on any GPU can hold.
func pickProbeMode(modes []drmModeInfo) (drmModeInfo, bool) {
	var fallback *drmModeInfo
	for i := range modes {
		m := &modes[i]
		if m.Hdisplay == 0 || m.Vdisplay == 0 || m.Hdisplay > vkmsProbeMaxDimension || m.Vdisplay > vkmsProbeMaxDimension {
			continue
		}
		if m.Type&drmModeTypePreferred != 0 {
			return *m, true
		}
		if fallback == nil {
			fallback = m
		}
	}
	if fallback == nil {
		return drmModeInfo{}, false
	}
	return *fallback, true
}

// probeVkmsScanout checks that vkms can show buffers exported by each GPU in
// gpus (their /dev/dri/cardN nodes). vkmsNode must be held from X (see the
// file comment), so this process becomes its DRM master. It returns nil when
// every GPU passed, a *vkmsScanoutError when the kernel refused one, and
// errVkmsProbeNotMaster when another program drives vkms.
func probeVkmsScanout(vkmsNode string, gpus []string) error {
	if len(gpus) == 0 {
		return errors.New("no GPU to test vkms against")
	}
	a, err := newPinnedArena(1 << 20)
	if err != nil {
		return err
	}
	defer a.free()
	vfd, err := unix.Open(vkmsNode, drmCloexecRDWR, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", vkmsNode, err)
	}
	defer unix.Close(vfd)
	target, err := vkmsFindTarget(vfd, a)
	if err != nil {
		return err
	}
	for _, gpu := range gpus {
		if err := probeVkmsWithGPU(vfd, a, target, gpu); err != nil {
			return err
		}
	}
	return nil
}

func probeVkmsWithGPU(vfd int, a *pinnedArena, t vkmsProbeTarget, gpu string) error {
	fail := func(step string, err error) error {
		if errors.Is(err, unix.EACCES) && (step == "setting a mode" || step == "page flip") {
			return errVkmsProbeNotMaster
		}
		return &vkmsScanoutError{GPU: gpu, Step: step, Err: err}
	}
	gfd, err := unix.Open(gpu, drmCloexecRDWR, 0)
	if err != nil {
		// Not a verdict on the kernel: the session cannot open this GPU.
		return fmt.Errorf("opening %s: %w", gpu, err)
	}
	defer unix.Close(gfd)

	width, height := uint32(t.mode.Hdisplay), uint32(t.mode.Vdisplay)
	dumb, err := pinned(a, drmModeCreateDumb{Width: width, Height: height, Bpp: 32})
	if err != nil {
		return err
	}
	if err := drmIoctl(gfd, drmIoctlCreateDumb, unsafe.Pointer(dumb)); err != nil {
		return fmt.Errorf("allocating a test buffer on %s: %w", gpu, err)
	}
	defer func() {
		h, _ := pinned(a, dumb.Handle)
		if h != nil {
			_ = drmIoctl(gfd, drmIoctlDestroyDumb, unsafe.Pointer(h))
		}
	}()

	exp, err := pinned(a, drmPrimeHandle{Handle: dumb.Handle, Flags: drmCloexecRDWR, Fd: -1})
	if err != nil {
		return err
	}
	if err := drmIoctl(gfd, drmIoctlPrimeHandleToFD, unsafe.Pointer(exp)); err != nil {
		exp.Flags = unix.O_CLOEXEC // older kernels refuse DRM_RDWR on some drivers
		if err2 := drmIoctl(gfd, drmIoctlPrimeHandleToFD, unsafe.Pointer(exp)); err2 != nil {
			return fail("exporting the GPU buffer", err)
		}
	}
	defer unix.Close(int(exp.Fd))

	imp, err := pinned(a, drmPrimeHandle{Fd: exp.Fd})
	if err != nil {
		return err
	}
	if err := drmIoctl(vfd, drmIoctlPrimeFDToHandle, unsafe.Pointer(imp)); err != nil {
		return fail("importing the GPU buffer into vkms", err)
	}
	defer func() {
		gc, _ := pinned(a, drmGemClose{Handle: imp.Handle})
		if gc != nil {
			_ = drmIoctl(vfd, drmIoctlGemClose, unsafe.Pointer(gc))
		}
	}()

	fb, err := pinned(a, drmModeFbCmd2{Width: width, Height: height, PixelFormat: drmFormatXRGB8888})
	if err != nil {
		return err
	}
	fb.Handles[0], fb.Pitches[0] = imp.Handle, dumb.Pitch
	if err := drmIoctl(vfd, drmIoctlAddFB2, unsafe.Pointer(fb)); err != nil {
		return fail("adding a framebuffer", err)
	}
	defer func() {
		id, _ := pinned(a, fb.FbID)
		if id != nil {
			_ = drmIoctl(vfd, drmIoctlRmFB, unsafe.Pointer(id))
		}
	}()

	conns, err := pinnedSlice[uint32](a, 1)
	if err != nil {
		return err
	}
	conns[0] = t.connector
	set, err := pinned(a, drmModeCrtc{
		SetConnectorsPtr: sliceAddr(conns), CountConnectors: 1,
		CrtcID: t.crtc, FbID: fb.FbID, ModeValid: 1, Mode: t.mode,
	})
	if err != nil {
		return err
	}
	// This is the call X makes, where vkms_prepare_fb maps the buffer.
	if err := drmIoctl(vfd, drmIoctlSetCrtc, unsafe.Pointer(set)); err != nil {
		return fail("setting a mode", err)
	}
	defer func() {
		off, _ := pinned(a, drmModeCrtc{CrtcID: t.crtc})
		if off != nil {
			_ = drmIoctl(vfd, drmIoctlSetCrtc, unsafe.Pointer(off))
		}
	}()

	// X then flips frames; check that a flip completes, so a commit that
	// never finishes (flip_done timeout) is caught here as well.
	flip, err := pinned(a, drmModeCrtcPageFlip{CrtcID: t.crtc, FbID: fb.FbID, Flags: drmModePageFlipEvent, UserData: 0x44535056})
	if err != nil {
		return err
	}
	if err := drmIoctl(vfd, drmIoctlPageFlip, unsafe.Pointer(flip)); err != nil {
		return fail("page flip", err)
	}
	if err := waitFlipComplete(vfd, vkmsProbeFlipTimeout); err != nil {
		return fail("page flip", err)
	}
	return nil
}

func waitFlipComplete(fd int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 1024)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return errors.New("the flip did not complete")
		}
		pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(pfd, int(left.Milliseconds())+1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		r, err := unix.Read(fd, buf)
		if err != nil {
			return err
		}
		for off := 0; off+8 <= r; {
			typ := *(*uint32)(unsafe.Pointer(&buf[off]))
			length := int(*(*uint32)(unsafe.Pointer(&buf[off+4])))
			if typ == drmEventFlipComplete {
				return nil
			}
			if length < 8 {
				break
			}
			off += length
		}
	}
}

// drmCardsUnder lists /dev/dri/cardN for DRM cards whose device path
// satisfies match.
func drmCardsUnder(sysDRM string, match func(devicePath string) bool) []string {
	var out []string
	entries, _ := filepath.Glob(filepath.Join(sysDRM, "card[0-9]*"))
	for _, dir := range entries {
		name := filepath.Base(dir)
		if strings.Contains(name, "-") {
			continue // a connector, not a card
		}
		dev, err := filepath.EvalSymlinks(filepath.Join(dir, "device"))
		if err != nil || !match(dev) {
			continue
		}
		out = append(out, filepath.Join("/dev/dri", name))
	}
	return out
}

// vkmsCardNode returns the /dev/dri node of the vkms device, if loaded.
func vkmsCardNode(sysDRM string) (string, bool) {
	cards := drmCardsUnder(sysDRM, func(dev string) bool {
		return strings.HasSuffix(dev, "/vkms") || strings.Contains(dev, "/vkms/")
	})
	if len(cards) == 0 {
		return "", false
	}
	return cards[0], true
}

// realGPUCards returns the PCI GPUs (the possible exporters of X's frames).
func realGPUCards(sysDRM string) []string {
	return drmCardsUnder(sysDRM, func(dev string) bool { return strings.Contains(dev, "/pci") })
}

// panicOnWarn: the kernel turns any WARN into a panic, and a refused vkms
// buffer WARNs, so even the probe would crash the machine.
func panicOnWarn() bool {
	b, err := os.ReadFile("/proc/sys/kernel/panic_on_warn")
	return err == nil && strings.TrimSpace(string(b)) != "0"
}
