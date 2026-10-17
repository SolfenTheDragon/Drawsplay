//go:build windows

package main

// Native shortcut (.lnk) creation via the IShellLinkW / IPersistFile COM
// interfaces. This replaces an earlier PowerShell + WScript.Shell approach:
// an installer spawning PowerShell to drop shortcuts is a common Windows
// Defender heuristic, and doing it in-process avoids that signal entirely.

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
)

// CLSID_ShellLink {00021401-0000-0000-C000-000000000046}
var clsidShellLink = windows.GUID{
	Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// IID_IShellLinkW {000214F9-0000-0000-C000-000000000046}
var iidIShellLinkW = windows.GUID{
	Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// IID_IPersistFile {0000010B-0000-0000-C000-000000000046}
var iidIPersistFile = windows.GUID{
	Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// iShellLinkWVtbl is the IShellLinkW vtable (Unicode). Only the methods used
// here are named; the rest are padded so offsets line up.
type iShellLinkWVtbl struct {
	QueryInterface      uintptr
	AddRef              uintptr
	Release             uintptr
	GetPath             uintptr
	GetIDList           uintptr
	SetIDList           uintptr
	GetDescription      uintptr
	SetDescription      uintptr
	GetWorkingDirectory uintptr
	SetWorkingDirectory uintptr
	GetArguments        uintptr
	SetArguments        uintptr
	GetHotkey           uintptr
	SetHotkey           uintptr
	GetShowCmd          uintptr
	SetShowCmd          uintptr
	GetIconLocation     uintptr
	SetIconLocation     uintptr
	SetRelativePath     uintptr
	Resolve             uintptr
	SetPath             uintptr
}

type iShellLinkW struct{ vtbl *iShellLinkWVtbl }

// iPersistFileVtbl is the IPersistFile vtable. Only Save/Release are used.
type iPersistFileVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetClassID     uintptr
	IsDirty        uintptr
	Load           uintptr
	Save           uintptr
	SaveCompleted  uintptr
	GetCurFile     uintptr
}

type iPersistFile struct{ vtbl *iPersistFileVtbl }

// createShortcut writes a .lnk at lnk pointing at target, with its working
// directory set to target's folder and the given description.
func createShortcut(lnk, target, description string) error {
	ret, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	// S_OK (0) or S_FALSE (1, already initialized) are both fine; only
	// uninitialize when we initialized.
	if ret == 0 || ret == 1 {
		defer procCoUninitialize.Call()
	}

	var psl *iShellLinkW
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&psl)))
	if hr != 0 || psl == nil {
		return fmt.Errorf("CoCreateInstance(ShellLink): hr=0x%x", uint32(hr))
	}
	defer syscall.SyscallN(psl.vtbl.Release, uintptr(unsafe.Pointer(psl)))

	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if hr, _, _ := syscall.SyscallN(psl.vtbl.SetPath,
		uintptr(unsafe.Pointer(psl)), uintptr(unsafe.Pointer(targetPtr))); hr != 0 {
		return fmt.Errorf("IShellLink.SetPath: hr=0x%x", uint32(hr))
	}
	workDir := target
	if i := lastPathSep(target); i >= 0 {
		workDir = target[:i]
	}
	if wdPtr, err := windows.UTF16PtrFromString(workDir); err == nil {
		syscall.SyscallN(psl.vtbl.SetWorkingDirectory,
			uintptr(unsafe.Pointer(psl)), uintptr(unsafe.Pointer(wdPtr)))
	}
	if description != "" {
		if dPtr, err := windows.UTF16PtrFromString(description); err == nil {
			syscall.SyscallN(psl.vtbl.SetDescription,
				uintptr(unsafe.Pointer(psl)), uintptr(unsafe.Pointer(dPtr)))
		}
	}

	var ppf *iPersistFile
	if hr, _, _ := syscall.SyscallN(psl.vtbl.QueryInterface,
		uintptr(unsafe.Pointer(psl)),
		uintptr(unsafe.Pointer(&iidIPersistFile)),
		uintptr(unsafe.Pointer(&ppf))); hr != 0 || ppf == nil {
		return fmt.Errorf("QueryInterface(IPersistFile): hr=0x%x", uint32(hr))
	}
	defer syscall.SyscallN(ppf.vtbl.Release, uintptr(unsafe.Pointer(ppf)))

	lnkPtr, err := windows.UTF16PtrFromString(lnk)
	if err != nil {
		return err
	}
	if hr, _, _ := syscall.SyscallN(ppf.vtbl.Save,
		uintptr(unsafe.Pointer(ppf)), uintptr(unsafe.Pointer(lnkPtr)), 1); hr != 0 {
		return fmt.Errorf("IPersistFile.Save: hr=0x%x", uint32(hr))
	}
	return nil
}

func lastPathSep(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '\\' || p[i] == '/' {
			return i
		}
	}
	return -1
}
