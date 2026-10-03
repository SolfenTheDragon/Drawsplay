//go:build windows

package main

// Parsec VDD (Virtual Display Adapter) driver control.
//
// The Parsec VDD is a signed IddCx indirect display driver whose virtual
// monitors are added/removed at runtime through DeviceIoControl. Constants
// and the ioctl calling convention follow the MIT-licensed reference header
// github.com/nomi-san/parsec-vdd (core/parsec-vdd.h):
//
//   - The control device is located by its device interface GUID
//     {00b41627-04c4-429e-a26e-0265cf50c8fa} via CM_Get_Device_Interface_List.
//   - Every ioctl passes a zeroed 32-byte input buffer (optionally carrying a
//     small payload) and receives one little-endian DWORD result, using
//     overlapped I/O with a 5-second wait.
//   - VDD_IOCTL_ADD returns the new display's index; VDD_IOCTL_REMOVE takes
//     that index as a 16-bit big-endian value.
//   - Displays stay alive only while VDD_IOCTL_UPDATE ("ping") arrives at
//     least every ~100 ms; vddDevice runs a 50 ms keepalive goroutine.
//
// The driver itself must be installed once by the user (see README).

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	vddIoctlAdd     = 0x0022e004 // CTL_CODE(FILE_DEVICE_UNKNOWN, 0x801, METHOD_BUFFERED, FILE_READ|FILE_WRITE)
	vddIoctlRemove  = 0x0022a008 // CTL_CODE(FILE_DEVICE_UNKNOWN, 0x802, METHOD_BUFFERED, FILE_WRITE)
	vddIoctlUpdate  = 0x0022a00c // CTL_CODE(FILE_DEVICE_UNKNOWN, 0x803, METHOD_BUFFERED, FILE_WRITE)
	vddIoctlVersion = 0x0022e010 // CTL_CODE(FILE_DEVICE_UNKNOWN, 0x804, METHOD_BUFFERED, FILE_READ|FILE_WRITE)

	vddKeepaliveInterval = 50 * time.Millisecond
	vddIoctlWaitMs       = 5000
)

// vddAdapterGUID is the device interface GUID of the Parsec VDD control
// device: {00b41627-04c4-429e-a26e-0265cf50c8fa}.
var vddAdapterGUID = windows.GUID{
	Data1: 0x00b41627,
	Data2: 0x04c4,
	Data3: 0x429e,
	Data4: [8]byte{0xa2, 0x6e, 0x02, 0x65, 0xcf, 0x50, 0xc8, 0xfa},
}

var (
	cfgmgr32                        = windows.NewLazySystemDLL("cfgmgr32.dll")
	procCMGetDevIfaceListSizeW      = cfgmgr32.NewProc("CM_Get_Device_Interface_List_SizeW")
	procCMGetDeviceInterfaceListW   = cfgmgr32.NewProc("CM_Get_Device_Interface_ListW")
	cmGetDeviceInterfaceListPresent = uintptr(0) // CM_GET_DEVICE_INTERFACE_LIST_PRESENT
)

// vddDevice is an open handle to the Parsec VDD control device with a running
// keepalive pinger. All methods are safe for use from one goroutine.
type vddDevice struct {
	handle        windows.Handle
	stopKeepalive chan struct{}
	keepaliveDone chan struct{}
	closeOnce     sync.Once
	ioMu          sync.Mutex
}

// openVDDDevice locates and opens the Parsec VDD control device, then starts
// the keepalive goroutine. Returns an error if the driver is not installed.
func openVDDDevice() (*vddDevice, error) {
	path, err := vddDevicePath()
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
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_WRITE_THROUGH,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	dev := &vddDevice{
		handle:        handle,
		stopKeepalive: make(chan struct{}),
		keepaliveDone: make(chan struct{}),
	}
	go dev.keepaliveLoop()
	return dev, nil
}

// vddDevicePath resolves the control device path from the interface GUID.
func vddDevicePath() (string, error) {
	var size uint32
	ret, _, _ := procCMGetDevIfaceListSizeW.Call(
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&vddAdapterGUID)),
		0,
		cmGetDeviceInterfaceListPresent,
	)
	if ret != 0 || size <= 1 {
		return "", fmt.Errorf("Parsec VDD device interface not found (driver installed?)")
	}

	buf := make([]uint16, size)
	ret, _, _ = procCMGetDeviceInterfaceListW.Call(
		uintptr(unsafe.Pointer(&vddAdapterGUID)),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(size),
		cmGetDeviceInterfaceListPresent,
	)
	if ret != 0 {
		return "", fmt.Errorf("CM_Get_Device_Interface_ListW failed: %d", ret)
	}

	// The list is REG_MULTI_SZ style; the first entry is the device path.
	path := windows.UTF16ToString(buf)
	if path == "" {
		return "", fmt.Errorf("Parsec VDD device interface list empty")
	}
	return path, nil
}

// Version returns the driver's minor version number.
func (d *vddDevice) Version() int {
	v, err := d.ioctl(vddIoctlVersion, nil)
	if err != nil {
		return -1
	}
	return int(v)
}

// AddDisplay plugs one virtual display and returns its driver-side index.
func (d *vddDevice) AddDisplay() (int, error) {
	idx, err := d.ioctl(vddIoctlAdd, nil)
	if err != nil {
		return -1, fmt.Errorf("VDD add display: %w", err)
	}
	d.ping()
	return int(idx), nil
}

// RemoveDisplay unplugs the virtual display with the given index.
func (d *vddDevice) RemoveDisplay(index int) {
	// The driver expects the index as 16-bit big-endian.
	var payload [2]byte
	binary.BigEndian.PutUint16(payload[:], uint16(index))
	_, _ = d.ioctl(vddIoctlRemove, payload[:])
	d.ping()
}

// Close stops the keepalive pinger and releases the device handle. Any
// remaining displays disappear once pings stop (driver-side timeout).
func (d *vddDevice) Close() {
	d.closeOnce.Do(func() {
		close(d.stopKeepalive)
		<-d.keepaliveDone
		_ = windows.CloseHandle(d.handle)
	})
}

// keepaliveLoop pings the driver every 50 ms; the driver drops all virtual
// displays if pings stop for ~100 ms, which doubles as crash cleanup.
func (d *vddDevice) keepaliveLoop() {
	defer close(d.keepaliveDone)
	ticker := time.NewTicker(vddKeepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopKeepalive:
			return
		case <-ticker.C:
			d.ping()
		}
	}
}

func (d *vddDevice) ping() {
	_, _ = d.ioctl(vddIoctlUpdate, nil)
}

// ioctl performs one overlapped DeviceIoControl per the VDD convention:
// 32-byte zeroed input (with optional payload prefix), DWORD output.
func (d *vddDevice) ioctl(code uint32, payload []byte) (uint32, error) {
	d.ioMu.Lock()
	defer d.ioMu.Unlock()

	var in [32]byte
	copy(in[:], payload)

	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)

	var out uint32
	var returned uint32
	overlapped := windows.Overlapped{HEvent: event}
	err = windows.DeviceIoControl(
		d.handle,
		code,
		&in[0],
		uint32(len(in)),
		(*byte)(unsafe.Pointer(&out)),
		uint32(unsafe.Sizeof(out)),
		&returned,
		&overlapped,
	)
	if err != nil && err != windows.ERROR_IO_PENDING {
		return 0, err
	}

	wait, err := windows.WaitForSingleObject(event, vddIoctlWaitMs)
	if err != nil {
		return 0, err
	}
	if wait != windows.WAIT_OBJECT_0 {
		if wait == 0x00000102 {
			return 0, windows.ERROR_TIMEOUT
		}
		return 0, fmt.Errorf("DeviceIoControl wait failed: %d", wait)
	}
	if err := windows.GetOverlappedResult(d.handle, &overlapped, &returned, false); err != nil {
		return 0, err
	}
	return out, nil
}
