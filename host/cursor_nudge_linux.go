//go:build linux

package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"time"
)

// nudgeX11PointerAfterCapture repeats the nudge while the capture pipeline
// starts (and falls back across encoders), since that also blanks the cursor.
func nudgeX11PointerAfterCapture() {
	for _, d := range []time.Duration{0, 1500 * time.Millisecond, 3 * time.Second, 5 * time.Second} {
		time.Sleep(d)
		nudgeX11Pointer()
	}
}

// nudgeX11Pointer wiggles the pointer by one pixel and back. Muffin leaves the
// pointer blank (a 1x1 transparent cursor) after the virtual output and the
// Drawsplay touch devices appear, until the next pointer motion event.
func nudgeX11Pointer() {
	display := os.Getenv("DISPLAY")
	if display == "" {
		return
	}
	path, err := exec.LookPath("xdotool")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, dx := range []string{"1", "-1"} {
		cmd := exec.CommandContext(ctx, path, "mousemove_relative", "--", dx, "0")
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		if err := cmd.Run(); err != nil {
			log.Printf("pointer nudge failed: %v", err)
			return
		}
	}
}
