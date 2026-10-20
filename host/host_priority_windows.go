//go:build windows

package main

import (
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	processPowerThrottling          = 4 // PROCESS_INFORMATION_CLASS ProcessPowerThrottling
	processPowerThrottlingVersion   = 1
	processPowerThrottlingExecSpeed = 0x1
)

type processPowerThrottlingState struct {
	Version     uint32
	ControlMask uint32
	StateMask   uint32
}

// raiseHostProcessPriority keeps the pump, socket writers and input injector
// from being starved by other desktop applications. The capture subprocess is
// already HIGH_PRIORITY_CLASS; without this the Go side that drains it ran at
// NORMAL and queued behind the very load the pipeline needs to outrun.
// Everything is best-effort: a refusal is logged and streaming continues.
func raiseHostProcessPriority() {
	proc, err := windows.GetCurrentProcess()
	if err != nil {
		log.Printf("Host priority: current process: %v", err)
		return
	}
	if err := windows.SetPriorityClass(proc, windows.ABOVE_NORMAL_PRIORITY_CLASS); err != nil {
		log.Printf("Host priority: SetPriorityClass: %v", err)
	}
	// 1 ms timer granularity keeps the pump's tickers and socket deadlines
	// accurate instead of snapping to the 15.6 ms default tick.
	if err := windows.TimeBeginPeriod(1); err != nil {
		log.Printf("Host priority: TimeBeginPeriod: %v", err)
	}
	// Opt out of EcoQoS/execution-speed throttling, which Windows applies to
	// background or tray-only processes and which also ignores the timer
	// request above.
	state := processPowerThrottlingState{
		Version:     processPowerThrottlingVersion,
		ControlMask: processPowerThrottlingExecSpeed,
		StateMask:   0,
	}
	setInfo := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetProcessInformation")
	if err := setInfo.Find(); err != nil {
		return
	}
	r, _, callErr := setInfo.Call(uintptr(proc), processPowerThrottling, uintptr(unsafe.Pointer(&state)), unsafe.Sizeof(state))
	if r == 0 {
		log.Printf("Host priority: disable power throttling: %v", callErr)
	}
}
