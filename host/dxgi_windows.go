//go:build windows

package main

// Minimal DXGI COM interop for display output enumeration.
//
// Used to discover which DXGI (adapter, output) pair a newly plugged Parsec
// VDD virtual display appears as — ffmpeg's ddagrab needs those indices —
// and the output's desktop rectangle for input coordinate mapping (M3).
//
// Only the three calls needed are wrapped (CreateDXGIFactory1 →
// EnumAdapters1 → EnumOutputs → GetDesc); vtable offsets follow the COM
// interface layouts of IDXGIFactory1/IDXGIAdapter1/IDXGIOutput.

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	dxgiDLL                = windows.NewLazySystemDLL("dxgi.dll")
	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")

	// IID_IDXGIFactory1 {770aae78-f26f-4dba-a829-253c83d1b387}
	iidIDXGIFactory1 = windows.GUID{
		Data1: 0x770aae78,
		Data2: 0xf26f,
		Data3: 0x4dba,
		Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87},
	}
)

const dxgiErrorNotFound = 0x887A0002

// COM vtable slot indices (after IUnknown 0-2, IDXGIObject 3-6).
const (
	vtblFactory1EnumAdapters1 = 12
	vtblAdapterEnumOutputs    = 7
	vtblAdapterGetDesc1       = 10
	vtblOutputGetDesc         = 7
	vtblRelease               = 2
)

type dxgiRect struct {
	Left, Top, Right, Bottom int32
}

type dxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates dxgiRect
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type dxgiAdapterDesc1 struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLuid           uint64
	Flags                 uint32
	_                     uint32 // struct padding to 8-byte boundary
}

// displayOutput describes one enumerated DXGI output.
type displayOutput struct {
	AdapterIndex int
	OutputIndex  int
	AdapterName  string
	VendorID     uint32 // PCI vendor of the adapter (see gpuVendor*)
	DeviceName   string // GDI name, e.g. \\.\DISPLAY3
	Attached     bool
	Rect         dxgiRect
}

// comObject is the memory layout of any COM interface pointer: the first
// word points at the vtable (an array of function pointers).
type comObject struct {
	vtbl *[64]uintptr
}

func comCall(obj *comObject, slot int, args ...uintptr) uintptr {
	full := append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)
	ret, _, _ := syscall.SyscallN(obj.vtbl[slot], full...)
	return ret
}

func comRelease(obj *comObject) {
	comCall(obj, vtblRelease)
}

// enumerateOutputs lists every DXGI output on every adapter. COM objects are
// released before returning; the result is plain data.
func enumerateOutputs() ([]displayOutput, error) {
	var factory *comObject
	hr, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&iidIDXGIFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if hr != 0 || factory == nil {
		return nil, fmt.Errorf("CreateDXGIFactory1 failed: 0x%08x", hr)
	}
	defer comRelease(factory)

	var results []displayOutput
	for a := 0; ; a++ {
		var adapter *comObject
		hr := comCall(factory, vtblFactory1EnumAdapters1, uintptr(a), uintptr(unsafe.Pointer(&adapter)))
		if uint32(hr) == dxgiErrorNotFound {
			break
		}
		if hr != 0 || adapter == nil {
			return nil, fmt.Errorf("EnumAdapters1(%d) failed: 0x%08x", a, hr)
		}

		var adapterDesc dxgiAdapterDesc1
		comCall(adapter, vtblAdapterGetDesc1, uintptr(unsafe.Pointer(&adapterDesc)))
		adapterName := windows.UTF16ToString(adapterDesc.Description[:])

		for o := 0; ; o++ {
			var output *comObject
			hr := comCall(adapter, vtblAdapterEnumOutputs, uintptr(o), uintptr(unsafe.Pointer(&output)))
			if uint32(hr) == dxgiErrorNotFound {
				break
			}
			if hr != 0 || output == nil {
				break
			}
			var desc dxgiOutputDesc
			comCall(output, vtblOutputGetDesc, uintptr(unsafe.Pointer(&desc)))
			results = append(results, displayOutput{
				AdapterIndex: a,
				OutputIndex:  o,
				AdapterName:  adapterName,
				VendorID:     adapterDesc.VendorID,
				DeviceName:   windows.UTF16ToString(desc.DeviceName[:]),
				Attached:     desc.AttachedToDesktop != 0,
				Rect:         desc.DesktopCoordinates,
			})
			comRelease(output)
		}
		comRelease(adapter)
	}
	return results, nil
}
