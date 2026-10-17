//go:build windows

package main

// DrawSplay IddCx virtual display driver control.
//
// DrawSplay ships its own IndirectDisplay (IddCx/UMDF) driver
// (drivers/windows/idd) so it no longer depends on the third-party Parsec
// VDD. The host opens the driver's device interface and plugs exactly one
// virtual monitor per streaming session at the connected tablet's native
// resolution (or the mode last picked in Windows Display Settings) and
// refresh rate.
//
// Protocol (shared with drivers/windows/idd/Public.h):
//
//   - The control device is located by its device interface GUID
//     {4f73af95-3814-4206-a651-125dfbb5e8f5} via CM_Get_Device_Interface_List.
//   - IOCTL_DRAWSPLAY_ADD_MONITOR takes a packed
//     {Width,Height,RefreshHz,NativeWidth,NativeHeight} (five little-endian
//     uint32; the driver also accepts the original 12-byte v1 layout) and
//     returns a packed {MonitorId} uint32.
//   - IOCTL_DRAWSPLAY_REMOVE_MONITOR takes a packed {MonitorId} uint32.
//   - IOCTL_DRAWSPLAY_REMOVE_ALL takes no payload.
//
// Unlike the Parsec VDD, no keepalive ping is required: the driver tracks the
// control handle and tears every monitor down automatically when the handle
// closes (graceful exit or host crash), so a crash never leaves an orphaned
// virtual display.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// CTL_CODE(FILE_DEVICE_UNKNOWN, 0x800+n, METHOD_BUFFERED, FILE_ANY_ACCESS).
	iddIoctlAddMonitor    = 0x00222000
	iddIoctlRemoveMonitor = 0x00222004
	iddIoctlRemoveAll     = 0x00222008

	// How long AddMonitor keeps retrying while the driver reports its
	// adapter as not yet initialized, and how often.
	iddNotReadyWait = 5 * time.Second
	iddNotReadyPoll = 100 * time.Millisecond
)

// iddInterfaceGUID is the device interface GUID of the DrawSplay IddCx control
// device: {4f73af95-3814-4206-a651-125dfbb5e8f5}.
var iddInterfaceGUID = windows.GUID{
	Data1: 0x4f73af95,
	Data2: 0x3814,
	Data3: 0x4206,
	Data4: [8]byte{0xa6, 0x51, 0x12, 0x5d, 0xfb, 0xb5, 0xe8, 0xf5},
}

var (
	cfgmgr32                        = windows.NewLazySystemDLL("cfgmgr32.dll")
	procCMGetDevIfaceListSizeW      = cfgmgr32.NewProc("CM_Get_Device_Interface_List_SizeW")
	procCMGetDeviceInterfaceListW   = cfgmgr32.NewProc("CM_Get_Device_Interface_ListW")
	cmGetDeviceInterfaceListPresent = uintptr(0) // CM_GET_DEVICE_INTERFACE_LIST_PRESENT
)

// iddDevice is an open handle to the DrawSplay IddCx control device.
type iddDevice struct {
	handle    windows.Handle
	ioMu      sync.Mutex
	closeOnce sync.Once
}

// openIDDDevice locates and opens the DrawSplay IddCx control device. Returns
// an error if the driver is not installed or the device is not yet ready.
func openIDDDevice() (*iddDevice, error) {
	path, err := iddDevicePath()
	if err != nil {
		return nil, err
	}

	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &iddDevice{handle: handle}, nil
}

// iddDevicePath resolves the control device path from the interface GUID.
func iddDevicePath() (string, error) {
	var size uint32
	ret, _, _ := procCMGetDevIfaceListSizeW.Call(
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&iddInterfaceGUID)),
		0,
		cmGetDeviceInterfaceListPresent,
	)
	if ret != 0 || size <= 1 {
		return "", fmt.Errorf("DrawSplay IddCx device interface not found (driver installed?)")
	}

	buf := make([]uint16, size)
	ret, _, _ = procCMGetDeviceInterfaceListW.Call(
		uintptr(unsafe.Pointer(&iddInterfaceGUID)),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(size),
		cmGetDeviceInterfaceListPresent,
	)
	if ret != 0 {
		return "", fmt.Errorf("CM_Get_Device_Interface_ListW failed: %d", ret)
	}

	path := windows.UTF16ToString(buf)
	if path == "" {
		return "", fmt.Errorf("DrawSplay IddCx device interface list empty")
	}
	return path, nil
}

// AddMonitor plugs one virtual monitor starting at width x height @ refreshHz
// (the preferred mode) and returns its driver-side monitor id. The monitor
// also offers nativeWidth x nativeHeight (the tablet panel; 0 = same as the
// initial mode) and the standard 720p-4K resolutions in Windows Display
// Settings. The driver clamps implausible values (640x480..7680x4320,
// 24..240 Hz).
func (d *iddDevice) AddMonitor(width, height, refreshHz, nativeWidth, nativeHeight int) (int, error) {
	in := encodeIDDAddMonitor(width, height, refreshHz, nativeWidth, nativeHeight)

	var out [4]byte
	returned, err := d.ioctl(iddIoctlAddMonitor, in[:], out[:])
	// The driver answers "not ready" until IddCx has finished initializing
	// its adapter (right after boot, driver install or a GPU driver reset).
	for deadline := time.Now().Add(iddNotReadyWait); errors.Is(err, windows.ERROR_NOT_READY) && time.Now().Before(deadline); {
		time.Sleep(iddNotReadyPoll)
		returned, err = d.ioctl(iddIoctlAddMonitor, in[:], out[:])
	}
	if err != nil {
		return -1, fmt.Errorf("IDD add monitor %dx%d@%d: %w", width, height, refreshHz, err)
	}
	if returned < uint32(len(out)) {
		return -1, fmt.Errorf("IDD add monitor: short response (%d bytes)", returned)
	}
	return int(binary.LittleEndian.Uint32(out[:])), nil
}

// encodeIDDAddMonitor packs the v2 ADD_MONITOR request. Drivers that only
// know the v1 layout read the first 12 bytes and ignore the native size.
func encodeIDDAddMonitor(width, height, refreshHz, nativeWidth, nativeHeight int) [20]byte {
	var in [20]byte
	binary.LittleEndian.PutUint32(in[0:], uint32(width))
	binary.LittleEndian.PutUint32(in[4:], uint32(height))
	binary.LittleEndian.PutUint32(in[8:], uint32(refreshHz))
	binary.LittleEndian.PutUint32(in[12:], uint32(max(nativeWidth, 0)))
	binary.LittleEndian.PutUint32(in[16:], uint32(max(nativeHeight, 0)))
	return in
}

// RemoveMonitor unplugs the virtual monitor with the given id.
func (d *iddDevice) RemoveMonitor(monitorID int) error {
	var in [4]byte
	binary.LittleEndian.PutUint32(in[0:], uint32(monitorID))
	if _, err := d.ioctl(iddIoctlRemoveMonitor, in[:], nil); err != nil {
		return fmt.Errorf("IDD remove monitor %d: %w", monitorID, err)
	}
	return nil
}

// RemoveAllMonitors unplugs every monitor created through this handle.
func (d *iddDevice) RemoveAllMonitors() error {
	if _, err := d.ioctl(iddIoctlRemoveAll, nil, nil); err != nil {
		return fmt.Errorf("IDD remove all monitors: %w", err)
	}
	return nil
}

// Close releases the device handle. The driver removes every monitor created
// through this handle as soon as it closes, so no explicit teardown is needed.
func (d *iddDevice) Close() {
	d.closeOnce.Do(func() {
		_ = windows.CloseHandle(d.handle)
	})
}

// ioctl performs one synchronous buffered DeviceIoControl and returns the
// number of bytes written to out.
func (d *iddDevice) ioctl(code uint32, in, out []byte) (uint32, error) {
	d.ioMu.Lock()
	defer d.ioMu.Unlock()

	var inPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	var outPtr *byte
	if len(out) > 0 {
		outPtr = &out[0]
	}
	var returned uint32
	err := windows.DeviceIoControl(
		d.handle,
		code,
		inPtr,
		uint32(len(in)),
		outPtr,
		uint32(len(out)),
		&returned,
		nil,
	)
	if err != nil {
		return 0, err
	}
	return returned, nil
}
