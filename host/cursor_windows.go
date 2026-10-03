//go:build windows

package main

// Host cursor transport. Desktop Duplication exposes the hardware pointer as
// metadata and may update it only a few times per second on an otherwise
// static desktop. Polling GetCursorInfo directly and rendering the cursor on
// Android decouples pointer latency from desktop damage.

import (
	"context"
	"time"
	"unsafe"
)

var procGetCursorInfo = user32.NewProc("GetCursorInfo")

const cursorShowing = 0x00000001

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Handle uintptr
	X      int32
	Y      int32
}

func runCursorPublisher(ctx context.Context, send func(hostCursorMsg)) {
	ticker := time.NewTicker(time.Second / 120)
	defer ticker.Stop()

	last := hostCursorMsg{Type: "cursor", X: -1, Y: -1}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		target := activeTarget()
		if target == nil || target.Rect.empty() {
			if last.Visible {
				last = hostCursorMsg{Type: "cursor", Visible: false}
				send(last)
			}
			continue
		}

		info := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
		ok, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&info)))
		inside := ok != 0 &&
			info.Flags&cursorShowing != 0 &&
			int(info.X) >= target.Rect.X &&
			int(info.X) < target.Rect.X+target.Rect.W &&
			int(info.Y) >= target.Rect.Y &&
			int(info.Y) < target.Rect.Y+target.Rect.H

		next := hostCursorMsg{Type: "cursor", Visible: inside}
		if inside {
			next.X = float64(int(info.X)-target.Rect.X) / float64(target.Rect.W)
			next.Y = float64(int(info.Y)-target.Rect.Y) / float64(target.Rect.H)
		}
		if next.Visible != last.Visible || next.X != last.X || next.Y != last.Y {
			send(next)
			last = next
		}
	}
}
