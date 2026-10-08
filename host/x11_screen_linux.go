//go:build linux

package main

// X screen size lookup for the x11grab mirror path, which records the whole
// root window but is never told its size.

import (
	"context"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// x11RootSizeProbe reports the size of the X screen that x11grab records, or
// ok=false when it cannot be determined. A variable so tests stay off the
// real X server.
var x11RootSizeProbe = cachedX11RootSize

// x11RootSizeTTL bounds reuse of a probed size. Pipelines are rebuilt on every
// bitrate step, so the answer is cached, but the user can resize the X screen
// (display settings, xrandr) at any time.
const x11RootSizeTTL = 15 * time.Second

const x11RootSizeTimeout = 2 * time.Second

type x11RootSize struct {
	width, height int
	ok            bool
	at            time.Time
}

var (
	x11RootSizeMu    sync.Mutex
	x11RootSizeCache = map[string]x11RootSize{}
)

func cachedX11RootSize(display string) (int, int, bool) {
	now := time.Now()
	x11RootSizeMu.Lock()
	entry, hit := x11RootSizeCache[display]
	x11RootSizeMu.Unlock()
	if hit && now.Sub(entry.at) < x11RootSizeTTL {
		return entry.width, entry.height, entry.ok
	}
	width, height, ok := queryX11RootSize(display)
	if !ok && !hit {
		log.Printf("X screen size of %s unknown (xdpyinfo/xrandr unavailable); sizing the software encoder conservatively", display)
	}
	x11RootSizeMu.Lock()
	x11RootSizeCache[display] = x11RootSize{width: width, height: height, ok: ok, at: now}
	x11RootSizeMu.Unlock()
	return width, height, ok
}

// queryX11RootSize asks the X server through the standard client tools
// (x11-utils, x11-xserver-utils), answering in a few milliseconds.
func queryX11RootSize(display string) (int, int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), x11RootSizeTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "xdpyinfo", "-display", display).Output(); err == nil {
		if width, height, ok := parseXdpyinfoDimensions(string(out)); ok {
			return width, height, true
		}
	}
	if out, err := exec.CommandContext(ctx, "xrandr", "--display", display, "--current").Output(); err == nil {
		if width, height, ok := parseXrandrCurrentSize(string(out)); ok {
			return width, height, true
		}
	}
	return 0, 0, false
}

var (
	xdpyinfoDefaultScreen = regexp.MustCompile(`default screen number:\s+(\d+)`)
	xdpyinfoDimensions    = regexp.MustCompile(`dimensions:\s+(\d+)x(\d+) pixels`)
	xrandrCurrentSize     = regexp.MustCompile(`current (\d+) x (\d+)`)
)

// parseXdpyinfoDimensions reads the default screen's "dimensions:" line.
func parseXdpyinfoDimensions(out string) (int, int, bool) {
	screen := "0"
	if m := xdpyinfoDefaultScreen.FindStringSubmatch(out); m != nil {
		screen = m[1]
	}
	if i := strings.Index(out, "screen #"+screen+":"); i >= 0 {
		out = out[i:]
	}
	return parseSizeMatch(xdpyinfoDimensions.FindStringSubmatch(out))
}

// parseXrandrCurrentSize reads "Screen N: minimum ..., current W x H, ...".
func parseXrandrCurrentSize(out string) (int, int, bool) {
	return parseSizeMatch(xrandrCurrentSize.FindStringSubmatch(out))
}

func parseSizeMatch(m []string) (int, int, bool) {
	if len(m) != 3 {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(m[1])
	height, errH := strconv.Atoi(m[2])
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}
