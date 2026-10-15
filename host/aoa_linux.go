//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Pure-Go usbfs backend: talks to /dev/bus/usb/BBB/DDD through ioctls, so no
// libusb or cgo is needed. The user needs read/write access to the device node
// (systemd's uaccess tag via a udev rule, see packaging/linux/70-drawsplay-aoa.rules).

type usbfsCtrlTransfer struct {
	RequestType uint8
	Request     uint8
	Value       uint16
	Index       uint16
	Length      uint16
	Timeout     uint32 // ms
	Data        unsafe.Pointer
}

type usbfsBulkTransfer struct {
	Endpoint uint32
	Length   uint32
	Timeout  uint32 // ms
	Data     unsafe.Pointer
}

const (
	iocWrite = 1
	iocRead  = 2
)

func usbfsIOC(dir, nr, size uintptr) uintptr {
	return dir<<30 | size<<16 | uintptr('U')<<8 | nr
}

var (
	usbfsControl        = usbfsIOC(iocRead|iocWrite, 0, unsafe.Sizeof(usbfsCtrlTransfer{}))
	usbfsBulk           = usbfsIOC(iocRead|iocWrite, 2, unsafe.Sizeof(usbfsBulkTransfer{}))
	usbfsClaimInterface = usbfsIOC(iocRead, 15, unsafe.Sizeof(uint32(0)))
	usbfsRelease        = usbfsIOC(iocRead, 16, unsafe.Sizeof(uint32(0)))
	usbfsClearHalt      = usbfsIOC(iocRead, 21, unsafe.Sizeof(uint32(0)))
)

const (
	aoaBulkReadSlice  = 250 // ms; a read re-arms after each timeout so Close is prompt
	aoaBulkWriteLimit = 5000
	aoaBulkChunk      = 16384
	usbSysfsRoot      = "/sys/bus/usb/devices"
)

func ioctlUint(fd int, req uintptr, v uint32) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(&v)))
	if errno != 0 {
		return errno
	}
	return nil
}

type usbfsDevice struct {
	f    *os.File
	path string
}

func (d *usbfsDevice) fd() int { return int(d.f.Fd()) }

func (d *usbfsDevice) Control(reqType, request byte, value, index uint16, data []byte) (int, error) {
	ct := usbfsCtrlTransfer{
		RequestType: reqType,
		Request:     request,
		Value:       value,
		Index:       index,
		Length:      uint16(len(data)),
		Timeout:     uint32(aoaControlTimeout / time.Millisecond),
	}
	if len(data) > 0 {
		ct.Data = unsafe.Pointer(&data[0])
	}
	n, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(d.fd()), usbfsControl, uintptr(unsafe.Pointer(&ct)))
	if errno != 0 {
		return 0, fmt.Errorf("usbfs control transfer on %s: %w", d.path, errno)
	}
	return int(n), nil
}

func (d *usbfsDevice) Close() error { return d.f.Close() }

func (d *usbfsDevice) OpenPipe() (aoaPipe, error) {
	raw, err := os.ReadFile(d.path)
	if err != nil {
		return nil, fmt.Errorf("read descriptors of %s: %w", d.path, err)
	}
	iface, err := parseAOAInterface(raw)
	if err != nil {
		return nil, err
	}
	if err := ioctlUint(d.fd(), usbfsClaimInterface, uint32(iface.Number)); err != nil {
		return nil, fmt.Errorf("claim accessory interface %d on %s: %w (is another program using the tablet?)", iface.Number, d.path, err)
	}
	return &usbfsPipe{dev: d, iface: iface}, nil
}

type usbfsPipe struct {
	dev    *usbfsDevice
	iface  aoaInterface
	closed atomic.Bool
	ops    sync.WaitGroup
	once   sync.Once
}

func (p *usbfsPipe) bulk(ep byte, buf []byte, timeoutMS int) (int, error) {
	bt := usbfsBulkTransfer{Endpoint: uint32(ep), Length: uint32(len(buf)), Timeout: uint32(timeoutMS)}
	if len(buf) > 0 {
		bt.Data = unsafe.Pointer(&buf[0])
	}
	n, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(p.dev.fd()), usbfsBulk, uintptr(unsafe.Pointer(&bt)))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

func (p *usbfsPipe) Read(b []byte) (int, error) {
	if len(b) > aoaBulkChunk {
		b = b[:aoaBulkChunk]
	}
	for {
		if p.closed.Load() {
			return 0, errors.New("accessory pipe closed")
		}
		p.ops.Add(1)
		n, err := p.bulk(p.iface.EpIn, b, aoaBulkReadSlice)
		p.ops.Done()
		switch {
		case err == nil:
			if n == 0 {
				continue // zero-length packet terminating a transfer
			}
			return n, nil
		case errors.Is(err, syscall.ETIMEDOUT):
			continue
		case errors.Is(err, syscall.EPIPE):
			_ = ioctlUint(p.dev.fd(), usbfsClearHalt, uint32(p.iface.EpIn))
			return 0, fmt.Errorf("accessory IN endpoint stalled: %w", err)
		default:
			return 0, fmt.Errorf("accessory read: %w", err)
		}
	}
}

func (p *usbfsPipe) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		if p.closed.Load() {
			return total, errors.New("accessory pipe closed")
		}
		chunk := b
		if len(chunk) > aoaBulkChunk {
			chunk = chunk[:aoaBulkChunk]
		}
		p.ops.Add(1)
		n, err := p.bulk(p.iface.EpOut, chunk, aoaBulkWriteLimit)
		p.ops.Done()
		if err != nil {
			return total, fmt.Errorf("accessory write: %w", err)
		}
		total += n
		b = b[n:]
		if aoaWriteNeedsZLP(n, p.iface.MaxPacketOut) {
			p.ops.Add(1)
			_, err := p.bulk(p.iface.EpOut, nil, aoaBulkWriteLimit)
			p.ops.Done()
			if err != nil {
				return total, fmt.Errorf("accessory zero-length write: %w", err)
			}
		}
	}
	return total, nil
}

func (p *usbfsPipe) Close() error {
	p.once.Do(func() {
		p.closed.Store(true)
		// Reads re-arm every aoaBulkReadSlice ms, so in-flight operations drain quickly.
		done := make(chan struct{})
		go func() { p.ops.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		_ = ioctlUint(p.dev.fd(), usbfsRelease, uint32(p.iface.Number))
		_ = p.dev.Close()
	})
	return nil
}

func readSysfsHex(path string) (uint16, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(v), true
}

func readSysfsInt(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	return v, err == nil
}

// aoaScanDevices lists attached USB devices of interest.
func aoaScanDevices() ([]aoaCandidate, error) {
	entries, err := filepath.Glob(filepath.Join(usbSysfsRoot, "*"))
	if err != nil {
		return nil, err
	}
	var out []aoaCandidate
	for _, dir := range entries {
		name := filepath.Base(dir)
		if strings.Contains(name, ":") || strings.HasPrefix(name, "usb") {
			continue // interface node or root hub
		}
		vid, ok1 := readSysfsHex(filepath.Join(dir, "idVendor"))
		pid, ok2 := readSysfsHex(filepath.Join(dir, "idProduct"))
		bus, ok3 := readSysfsInt(filepath.Join(dir, "busnum"))
		dev, ok4 := readSysfsInt(filepath.Join(dir, "devnum"))
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		cand := aoaCandidate{VID: vid, PID: pid, Key: fmt.Sprintf("%s@%d", name, dev)}
		if !cand.inAccessoryMode() && !aoaVendorProbeAllowed(vid) {
			continue
		}
		node := fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, dev)
		cand.Open = func() (aoaDeviceHandle, error) {
			f, err := os.OpenFile(node, os.O_RDWR, 0)
			if err != nil {
				if errors.Is(err, os.ErrPermission) {
					return nil, fmt.Errorf("no permission to open %s (install packaging/linux/70-drawsplay-aoa.rules and replug): %w", node, err)
				}
				return nil, err
			}
			return &usbfsDevice{f: f, path: node}, nil
		}
		out = append(out, cand)
	}
	return out, nil
}
