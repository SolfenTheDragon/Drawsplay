//go:build windows

package main

// DPI awareness. Without this the process is DPI-virtualized: on scaled
// displays (e.g. 200%) GetSystemMetrics, GDI and DXGI report logical
// coordinates while InjectSyntheticPointerInput expects physical pixels, so
// injected input lands scaled toward the top-left. Declaring per-monitor-v2
// awareness makes every coordinate the host sees physical.

import (
	"log"

	"golang.org/x/sys/windows"
)

var (
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
)

// dpiAwarenessContextPerMonitorAwareV2 = DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4).
const dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3) // uintptr(-4)

// declareDPIAware opts the process into per-monitor DPI awareness, falling
// back to system awareness on older Windows. Must run before any display
// enumeration or input injection.
func declareDPIAware() {
	if err := procSetProcessDpiAwarenessContext.Find(); err == nil {
		ret, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2)
		if ret != 0 {
			return
		}
	}
	ret, _, _ := procSetProcessDPIAware.Call()
	if ret == 0 {
		log.Printf("Failed to declare DPI awareness (%v); input mapping may be wrong on scaled displays", windows.GetLastError())
	}
}
