//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// WinUSB backend. Windows cannot open a USB device without a bound driver, so
// both the tablet's original device (to send the AOA switch requests) and the
// accessory device (18d1:2d00-2d05) must have WinUSB bound, for example with
// libwdi or a driver package from the installer (see packaging/windows/README.md).
// Devices without WinUSB are reported with an actionable log line and skipped.

var (
	setupapiDLL = windows.NewLazySystemDLL("setupapi.dll")
	winusbDLL   = windows.NewLazySystemDLL("winusb.dll")

	procSetupDiOpenDevRegKey = setupapiDLL.NewProc("SetupDiOpenDevRegKey")

	procWinUsbInitialize    = winusbDLL.NewProc("WinUsb_Initialize")
	procWinUsbFree          = winusbDLL.NewProc("WinUsb_Free")
	procWinUsbControl       = winusbDLL.NewProc("WinUsb_ControlTransfer")
	procWinUsbReadPipe      = winusbDLL.NewProc("WinUsb_ReadPipe")
	procWinUsbWritePipe     = winusbDLL.NewProc("WinUsb_WritePipe")
	procWinUsbSetPipePolicy = winusbDLL.NewProc("WinUsb_SetPipePolicy")
	procWinUsbQueryIface    = winusbDLL.NewProc("WinUsb_QueryInterfaceSettings")
	procWinUsbQueryPipe     = winusbDLL.NewProc("WinUsb_QueryPipe")
	procWinUsbAbortPipe     = winusbDLL.NewProc("WinUsb_AbortPipe")
)

const (
	winusbPolicyShortPacketTerminate = 0x01
	winusbPolicyPipeTransferTimeout  = 0x03
	winusbPipeTypeBulk               = 2

	winusbReadSliceMS   = 250
	winusbWriteLimitMS  = 5000
	aoaBulkChunkWindows = 16384

	errorSemTimeout = 121
	dicsFlagGlobal  = 0x1
	direvDev        = 0x1
)

type winusbInterfaceDescriptor struct {
	Length, DescriptorType, InterfaceNumber, AlternateSetting       uint8
	NumEndpoints, InterfaceClass, InterfaceSubClass, InterfaceProto uint8
	Interface                                                       uint8
}

type winusbPipeInformation struct {
	PipeType          int32
	PipeID            uint8
	MaximumPacketSize uint16
	Interval          uint8
}

type winusbDevice struct {
	file  windows.Handle
	iface uintptr
	path  string
}

func openWinUSB(path string) (*winusbDevice, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	var iface uintptr
	r, _, callErr := procWinUsbInitialize.Call(uintptr(h), uintptr(unsafe.Pointer(&iface)))
	if r == 0 {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("WinUsb_Initialize %s: %w (bind the WinUSB driver to this device)", path, callErr)
	}
	return &winusbDevice{file: h, iface: iface, path: path}, nil
}

func (d *winusbDevice) Control(reqType, request byte, value, index uint16, data []byte) (int, error) {
	setup := uint64(reqType) | uint64(request)<<8 | uint64(value)<<16 | uint64(index)<<32 | uint64(len(data))<<48
	var buf uintptr
	if len(data) > 0 {
		buf = uintptr(unsafe.Pointer(&data[0]))
	}
	var transferred uint32
	var r uintptr
	var callErr error
	// WINUSB_SETUP_PACKET is passed by value: one register-sized word on
	// 64-bit, two stack words on 32-bit.
	if unsafe.Sizeof(uintptr(0)) == 8 {
		r, _, callErr = procWinUsbControl.Call(d.iface, uintptr(setup), buf, uintptr(len(data)),
			uintptr(unsafe.Pointer(&transferred)), 0)
	} else {
		r, _, callErr = procWinUsbControl.Call(d.iface, uintptr(uint32(setup)), uintptr(uint32(setup>>32)), buf,
			uintptr(len(data)), uintptr(unsafe.Pointer(&transferred)), 0)
	}
	if r == 0 {
		return 0, fmt.Errorf("WinUsb_ControlTransfer on %s: %w", d.path, callErr)
	}
	return int(transferred), nil
}

func (d *winusbDevice) Close() error {
	procWinUsbFree.Call(d.iface)
	return windows.CloseHandle(d.file)
}

func (d *winusbDevice) OpenPipe() (aoaPipe, error) {
	var desc winusbInterfaceDescriptor
	if r, _, err := procWinUsbQueryIface.Call(d.iface, 0, uintptr(unsafe.Pointer(&desc))); r == 0 {
		return nil, fmt.Errorf("WinUsb_QueryInterfaceSettings: %w", err)
	}
	p := &winusbPipe{dev: d}
	for i := 0; i < int(desc.NumEndpoints); i++ {
		var info winusbPipeInformation
		if r, _, _ := procWinUsbQueryPipe.Call(d.iface, 0, uintptr(i), uintptr(unsafe.Pointer(&info))); r == 0 {
			continue
		}
		if info.PipeType != winusbPipeTypeBulk {
			continue
		}
		if info.PipeID&0x80 != 0 && p.epIn == 0 {
			p.epIn = info.PipeID
		} else if info.PipeID&0x80 == 0 && p.epOut == 0 {
			p.epOut = info.PipeID
		}
	}
	if p.epIn == 0 || p.epOut == 0 {
		return nil, errors.New("accessory bulk endpoints not found (is WinUSB bound to the accessory interface?)")
	}
	setPolicy := func(pipe uint8, policy uint32, value uint32) {
		procWinUsbSetPipePolicy.Call(d.iface, uintptr(pipe), uintptr(policy), 4, uintptr(unsafe.Pointer(&value)))
	}
	setPolicy(p.epIn, winusbPolicyPipeTransferTimeout, winusbReadSliceMS)
	setPolicy(p.epOut, winusbPolicyPipeTransferTimeout, winusbWriteLimitMS)
	// Terminate transfers that are an exact multiple of the packet size with a
	// zero-length packet; otherwise the Android reader would stall.
	setPolicy(p.epOut, winusbPolicyShortPacketTerminate, 1)
	return p, nil
}

type winusbPipe struct {
	dev         *winusbDevice
	epIn, epOut uint8
	closed      atomic.Bool
	ops         sync.WaitGroup
	once        sync.Once
}

func (p *winusbPipe) Read(b []byte) (int, error) {
	if len(b) > aoaBulkChunkWindows {
		b = b[:aoaBulkChunkWindows]
	}
	for {
		if p.closed.Load() {
			return 0, errors.New("accessory pipe closed")
		}
		var n uint32
		p.ops.Add(1)
		r, _, err := procWinUsbReadPipe.Call(p.dev.iface, uintptr(p.epIn), uintptr(unsafe.Pointer(&b[0])),
			uintptr(len(b)), uintptr(unsafe.Pointer(&n)), 0)
		p.ops.Done()
		if r != 0 {
			if n == 0 {
				continue
			}
			return int(n), nil
		}
		if errno, ok := err.(syscall.Errno); ok && errno == errorSemTimeout {
			continue
		}
		return 0, fmt.Errorf("accessory read: %w", err)
	}
}

func (p *winusbPipe) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		if p.closed.Load() {
			return total, errors.New("accessory pipe closed")
		}
		chunk := b
		if len(chunk) > aoaBulkChunkWindows {
			chunk = chunk[:aoaBulkChunkWindows]
		}
		var n uint32
		p.ops.Add(1)
		r, _, err := procWinUsbWritePipe.Call(p.dev.iface, uintptr(p.epOut), uintptr(unsafe.Pointer(&chunk[0])),
			uintptr(len(chunk)), uintptr(unsafe.Pointer(&n)), 0)
		p.ops.Done()
		if r == 0 {
			return total, fmt.Errorf("accessory write: %w", err)
		}
		total += int(n)
		b = b[n:]
	}
	return total, nil
}

func (p *winusbPipe) Close() error {
	p.once.Do(func() {
		p.closed.Store(true)
		procWinUsbAbortPipe.Call(p.dev.iface, uintptr(p.epIn))
		done := make(chan struct{})
		go func() { p.ops.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		_ = p.dev.Close()
	})
	return nil
}

func parseUSBHardwareID(ids []string) (vid, pid uint16, ok bool) {
	for _, id := range ids {
		up := strings.ToUpper(id)
		if !strings.HasPrefix(up, "USB\\VID_") {
			continue
		}
		var v, p uint32
		if _, err := fmt.Sscanf(up, "USB\\VID_%04X&PID_%04X", &v, &p); err == nil {
			return uint16(v), uint16(p), true
		}
	}
	return 0, 0, false
}

// aoaScanDevices lists attached USB devices of interest that have a WinUSB
// interface registered. Devices of interest without WinUSB are reported once.
func aoaScanDevices() ([]aoaCandidate, error) {
	set, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_PRESENT|windows.DIGCF_ALLCLASSES, 0, "")
	if err != nil {
		return nil, fmt.Errorf("enumerate USB devices: %w", err)
	}
	defer set.Close()

	var out []aoaCandidate
	for i := 0; ; i++ {
		info, err := windows.SetupDiEnumDeviceInfo(set, i)
		if err != nil {
			break
		}
		raw, err := windows.SetupDiGetDeviceRegistryProperty(set, info, windows.SPDRP_HARDWAREID)
		if err != nil {
			continue
		}
		ids, _ := raw.([]string)
		vid, pid, ok := parseUSBHardwareID(ids)
		if !ok {
			continue
		}
		cand := aoaCandidate{VID: vid, PID: pid}
		if !cand.inAccessoryMode() && !aoaVendorProbeAllowed(vid) {
			continue
		}
		instance, err := windows.SetupDiGetDeviceInstanceId(set, info)
		if err != nil {
			continue
		}
		paths := winusbInterfacePaths(set, info, instance)
		if len(paths) == 0 {
			if ensureWinUSBDriver(vid, pid) {
				// Rebinding is asynchronous; the next scan picks the device up.
				log.Printf("AOA: WinUSB installed for %04x:%04x; waiting for the device to rebind", vid, pid)
			} else {
				throttledLog("aoa-nowinusb-"+instance, "AOA: %04x:%04x has no WinUSB interface; bind the WinUSB driver to it (see packaging/windows/README.md)", vid, pid)
			}
			continue
		}
		cand.Key = instance
		path := paths[0]
		cand.Open = func() (aoaDeviceHandle, error) { return openWinUSB(path) }
		out = append(out, cand)
	}
	return out, nil
}

// winusbInterfacePaths returns the device interface paths registered by the
// device's INF through its DeviceInterfaceGUIDs value.
func winusbInterfacePaths(set windows.DevInfo, info *windows.DevInfoData, instance string) []string {
	hkey, _, _ := procSetupDiOpenDevRegKey.Call(uintptr(set), uintptr(unsafe.Pointer(info)), dicsFlagGlobal, 0, direvDev, uintptr(registry.QUERY_VALUE))
	if hkey == 0 || hkey == ^uintptr(0) {
		return nil
	}
	key := registry.Key(hkey)
	defer key.Close()
	guids, _, err := key.GetStringsValue("DeviceInterfaceGUIDs")
	if err != nil {
		return nil
	}
	var paths []string
	for _, g := range guids {
		guid, err := windows.GUIDFromString(g)
		if err != nil {
			continue
		}
		list, err := windows.CM_Get_Device_Interface_List(instance, &guid, 0)
		if err == nil {
			paths = append(paths, list...)
		}
	}
	return paths
}
