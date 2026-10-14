//go:build linux

package main

// X11 extended display.
//
// X11 desktops (Cinnamon, MATE, Xfce, ...) have no API for creating a
// virtual monitor, so the extended desktop is built from a virtual RandR
// output that is always connected but drives no screen: the vkms kernel
// module ("sudo modprobe vkms") registers a virtual DRM device whose
// connector the X server's modesetting driver exposes as "Virtual-1-1"
// (xf86-video-intel's VIRTUAL1..n outputs behave the same way). For each
// video session the host:
//
//  1. adds the configured resolution (and the tablet's native resolution)
//     to the output's mode list, generating CVT reduced-blanking timings
//     when the output does not already offer that size;
//  2. switches the output on next to the other monitors with xrandr, so the
//     desktop treats it as a real second monitor (windows, panels, display
//     settings);
//  3. records only that output's rectangle of the X screen with x11grab
//     (x11GrabCaptureArgs in pipeline_linux.go) and points touch/pen input
//     at the output (x11_tablet_linux.go, tabletAbsCoords in
//     inject_linux.go).
//
// Resolution and position are OS-managed, as on GNOME Wayland: the user
// changes them in the desktop's display settings, and a watcher notices the
// new geometry, saves a changed resolution as the configured one and
// restarts the session so capture follows.
//
// The output is switched off x11ReleaseGrace after a session ends (so a
// reconnect does not shuffle windows between monitors) and when the host
// exits. Without a usable virtual output newVirtualDisplay falls back to
// mirroring the whole X screen.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// x11OutputEnv names the RandR output to extend onto when automatic
// detection does not find it (e.g. an evdi/DisplayLink output).
const x11OutputEnv = "DRAWSPLAY_X11_OUTPUT"

const (
	xrandrTimeout = 5 * time.Second

	// x11ApplyAttempts bounds how often a layout is re-applied when the
	// window manager reconfigures the screen underneath it: a mode-list
	// change is a hotplug to Muffin/Mutter, which then applies its own
	// layout.
	x11ApplyAttempts = 3

	// x11ModeSuffix marks the modes the host creates.
	x11ModeSuffix = "_drawsplay"

	// x11DefaultWidth/Height apply when no resolution is configured.
	x11DefaultWidth  = 1920
	x11DefaultHeight = 1080
)

// Timing knobs; variables so tests run without real waits.
var (
	// x11HotplugWait lets the window manager finish reacting to a mode-list
	// change before the host applies its own layout.
	x11HotplugWait = 400 * time.Millisecond
	// x11SettlePoll and x11SettleRounds bound the wait for a layout to stick
	// after a mode-list change.
	x11SettlePoll   = 250 * time.Millisecond
	x11SettleRounds = 6
	// x11WatchInterval is how often a session checks the output's geometry.
	x11WatchInterval = 2 * time.Second
	// x11ReleaseGrace keeps the output on across a quick reconnect.
	x11ReleaseGrace = 5 * time.Second
	// x11SwitchOffAttempts bounds the passes made over an unclaimed output:
	// the first after x11ReleaseGrace, then one every x11SwitchOffRetry
	// until it is confirmed off, which also catches a failed switch-off and
	// a window manager that switches the output back on.
	x11SwitchOffAttempts = 30
)

// x11SwitchOffRetry is the delay between passes after the first. A variable
// so tests need not wait.
var x11SwitchOffRetry = time.Second

var errNoX11VirtualOutput = errors.New("no virtual RandR output found; load the vkms kernel module (sudo modprobe vkms) to extend the desktop onto the tablet")

// x11AutoOutputPattern matches the virtual outputs used without
// configuration: vkms on a secondary GPU screen of the modesetting driver
// (Virtual-<gpu>-<n>; a primary-GPU Virtual-<n> is a VM's real display) and
// the intel driver's VIRTUAL<n> heads.
var x11AutoOutputPattern = regexp.MustCompile(`^(Virtual-\d+-\d+|VIRTUAL\d+)$`)

// xrandrRun runs xrandr against an X display and returns its stdout. A
// variable so tests stay off the real X server.
var xrandrRun = runXrandr

func runXrandr(display string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), xrandrTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xrandr", append([]string{"--display", display}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	// Kept apart from stdout: xrandr also warns on stderr when it succeeds
	// (e.g. about gamma on vkms outputs).
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.Join(strings.Fields(stderr.String()), " ")
		if len(detail) > 300 {
			detail = detail[:300] + "..."
		}
		if detail != "" {
			return stdout.String(), fmt.Errorf("xrandr %s: %w (%s)", strings.Join(args, " "), err, detail)
		}
		return stdout.String(), fmt.Errorf("xrandr %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// queryXrandr reads the current RandR state without re-probing outputs.
func queryXrandr(display string) (xrandrState, error) {
	out, err := xrandrRun(display, "--current")
	if err != nil {
		return xrandrState{}, err
	}
	return parseXrandrQuery(out)
}

type xrandrMode struct {
	Name       string
	W, H       int
	Interlaced bool
	Current    bool
	Preferred  bool
}

type xrandrOutput struct {
	Name      string
	Connected bool
	Primary   bool
	// Active outputs drive a CRTC; Rect is their area of the X screen.
	Active bool
	Rect   displayRect
	Modes  []xrandrMode
}

type xrandrState struct {
	ScreenW, ScreenH int
	MaxW, MaxH       int
	Outputs          []xrandrOutput
}

func (st xrandrState) output(name string) (xrandrOutput, bool) {
	for _, o := range st.Outputs {
		if o.Name == name {
			return o, true
		}
	}
	return xrandrOutput{}, false
}

var (
	xrandrScreenLine = regexp.MustCompile(`^Screen \d+: .*current (\d+) x (\d+), maximum (\d+) x (\d+)`)
	xrandrOutputLine = regexp.MustCompile(`^(\S+) (connected|disconnected|unknown connection)( primary)?(?: (\d+)x(\d+)\+(-?\d+)\+(-?\d+))?`)
	xrandrModeName   = regexp.MustCompile(`^(\d+)x(\d+)(i?)`)
)

// parseXrandrQuery reads `xrandr --current` output for the first screen.
func parseXrandrQuery(out string) (xrandrState, error) {
	var st xrandrState
	screenSeen := false
	current := -1
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if current < 0 {
				continue
			}
			if strings.Contains(line, "(0x") {
				// Verbose mode description: xrandr lists modes that no
				// output uses this way after the last output.
				current = -1
				continue
			}
			fields := strings.Fields(line)
			m := xrandrModeName.FindStringSubmatch(fields[0])
			if m == nil {
				continue
			}
			w, _ := strconv.Atoi(m[1])
			h, _ := strconv.Atoi(m[2])
			flags := strings.Join(fields[1:], " ")
			st.Outputs[current].Modes = append(st.Outputs[current].Modes, xrandrMode{
				Name:       fields[0],
				W:          w,
				H:          h,
				Interlaced: m[3] == "i",
				Current:    strings.Contains(flags, "*"),
				Preferred:  strings.Contains(flags, "+"),
			})
			continue
		}
		if m := xrandrScreenLine.FindStringSubmatch(line); m != nil {
			if screenSeen {
				break
			}
			screenSeen = true
			st.ScreenW, _ = strconv.Atoi(m[1])
			st.ScreenH, _ = strconv.Atoi(m[2])
			st.MaxW, _ = strconv.Atoi(m[3])
			st.MaxH, _ = strconv.Atoi(m[4])
			current = -1
			continue
		}
		if m := xrandrOutputLine.FindStringSubmatch(line); m != nil && screenSeen {
			o := xrandrOutput{Name: m[1], Connected: m[2] == "connected", Primary: m[3] != ""}
			if m[4] != "" {
				o.Active = true
				o.Rect.W, _ = strconv.Atoi(m[4])
				o.Rect.H, _ = strconv.Atoi(m[5])
				o.Rect.X, _ = strconv.Atoi(m[6])
				o.Rect.Y, _ = strconv.Atoi(m[7])
			}
			st.Outputs = append(st.Outputs, o)
			current = len(st.Outputs) - 1
			continue
		}
		current = -1
	}
	if !screenSeen {
		return st, errors.New("xrandr reported no screen")
	}
	return st, nil
}

// pickX11VirtualOutput chooses the output to extend onto: the named one, or
// the first virtual output found. It must not be the primary display, and
// another display has to stay active next to it.
func pickX11VirtualOutput(st xrandrState, name string) (xrandrOutput, error) {
	var pick xrandrOutput
	found := false
	if name != "" {
		o, ok := st.output(name)
		if !ok {
			return xrandrOutput{}, fmt.Errorf("RandR output %s not found", name)
		}
		if o.Primary {
			return xrandrOutput{}, fmt.Errorf("RandR output %s is the primary display", name)
		}
		pick, found = o, true
	} else {
		primary := ""
		for _, o := range st.Outputs {
			if !x11AutoOutputPattern.MatchString(o.Name) {
				continue
			}
			// vkms always reports connected; the intel driver's VIRTUAL
			// heads report disconnected until they are given a mode.
			if strings.HasPrefix(o.Name, "Virtual-") && !o.Connected {
				continue
			}
			if o.Primary {
				primary = o.Name
				continue
			}
			pick, found = o, true
			break
		}
		if !found && primary != "" {
			return xrandrOutput{}, fmt.Errorf("RandR output %s is the primary display", primary)
		}
	}
	if !found {
		return xrandrOutput{}, errNoX11VirtualOutput
	}
	for _, o := range st.Outputs {
		if o.Name != pick.Name && o.Active {
			return pick, nil
		}
	}
	return xrandrOutput{}, fmt.Errorf("RandR output %s is the only active display", pick.Name)
}

// findX11Mode returns a progressive mode of the given size on the output.
func findX11Mode(o xrandrOutput, w, h int) (string, bool) {
	for _, m := range o.Modes {
		if m.W == w && m.H == h && !m.Interlaced {
			return m.Name, true
		}
	}
	return "", false
}

func x11ModeName(w, h int) string {
	return fmt.Sprintf("%dx%d%s", w, h, x11ModeSuffix)
}

// ensureX11Mode makes a w x h mode available on the output and reports
// whether the output's mode list changed.
func ensureX11Mode(display string, o xrandrOutput, w, h int) (string, bool, error) {
	if name, ok := findX11Mode(o, w, h); ok {
		return name, false, nil
	}
	name := x11ModeName(w, h)
	args := append([]string{"--newmode", name}, cvtReducedBlanking(w, h).xrandrArgs()...)
	// BadName: the mode exists already, created by an earlier session.
	if _, err := xrandrRun(display, args...); err != nil && !strings.Contains(err.Error(), "BadName") {
		return "", false, err
	}
	if _, err := xrandrRun(display, "--addmode", o.Name, name); err != nil {
		return "", false, err
	}
	log.Printf("X11 extended display: added mode %s to %s", name, o.Name)
	return name, true, nil
}

// cvtTimings is a VESA CVT reduced-blanking (v1) 60 Hz mode, as computed by
// `cvt -r`, except that the width is not rounded down to a multiple of 8, so
// the mode matches the tablet exactly. A virtual output has no timing
// constraints; the timings only have to be valid.
type cvtTimings struct {
	ClockKHz                               int64
	HDisplay, HSyncStart, HSyncEnd, HTotal int
	VDisplay, VSyncStart, VSyncEnd, VTotal int
}

func cvtReducedBlanking(w, h int) cvtTimings {
	const (
		minVBlankMicros = 460.0
		hBlank          = 160
		hSync           = 32
		vFrontPorch     = 3
		minVBackPorch   = 6
		clockStepKHz    = 250
	)
	// Vertical sync width encodes the aspect ratio.
	vsync := 10
	switch {
	case h%3 == 0 && h*4/3 == w:
		vsync = 4
	case h%9 == 0 && h*16/9 == w:
		vsync = 5
	case h%10 == 0 && h*16/10 == w:
		vsync = 6
	case h%4 == 0 && h*5/4 == w:
		vsync = 7
	case h%9 == 0 && h*15/9 == w:
		vsync = 7
	}
	// float32 like libxcvt, so the results match `cvt -r` exactly.
	hPeriod := float32(1e6/60.0-minVBlankMicros) / float32(h)
	vbi := int(float32(minVBlankMicros)/hPeriod + 1)
	if vbi < vFrontPorch+vsync+minVBackPorch {
		vbi = vFrontPorch + vsync + minVBackPorch
	}
	t := cvtTimings{
		HDisplay:   w,
		HSyncEnd:   w + hBlank/2,
		HSyncStart: w + hBlank/2 - hSync,
		HTotal:     w + hBlank,
		VDisplay:   h,
		VSyncStart: h + vFrontPorch,
		VSyncEnd:   h + vFrontPorch + vsync,
		VTotal:     h + vbi,
	}
	t.ClockKHz = int64(float64(t.HTotal) * 1000.0 / float64(hPeriod))
	t.ClockKHz -= t.ClockKHz % clockStepKHz
	return t
}

// xrandrArgs is the timing part of an `xrandr --newmode` command line.
func (t cvtTimings) xrandrArgs() []string {
	return []string{
		fmt.Sprintf("%.2f", float64(t.ClockKHz)/1000),
		strconv.Itoa(t.HDisplay), strconv.Itoa(t.HSyncStart), strconv.Itoa(t.HSyncEnd), strconv.Itoa(t.HTotal),
		strconv.Itoa(t.VDisplay), strconv.Itoa(t.VSyncStart), strconv.Itoa(t.VSyncEnd), strconv.Itoa(t.VTotal),
		"+hsync", "-vsync",
	}
}

// x11Placement is where the extended display sits relative to the bounding
// box of the other displays: on which side, and how far along that edge.
type x11Placement struct {
	Side   string // "right", "left", "below" or "above"
	Offset int
}

type x11OutputPos struct {
	Name string
	X, Y int
}

type x11Layout struct {
	// Keep: the output already shows the wanted size; leave the layout as
	// the user (or a previous session) arranged it.
	Keep bool
	Rect displayRect
	// Moves shift other outputs so the desktop keeps its origin at 0,0.
	Moves []x11OutputPos
}

func boundsOf(rects []displayRect) displayRect {
	if len(rects) == 0 {
		return displayRect{}
	}
	minX, minY := rects[0].X, rects[0].Y
	maxX, maxY := rects[0].X+rects[0].W, rects[0].Y+rects[0].H
	for _, r := range rects[1:] {
		minX = min(minX, r.X)
		minY = min(minY, r.Y)
		maxX = max(maxX, r.X+r.W)
		maxY = max(maxY, r.Y+r.H)
	}
	return displayRect{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
}

// placementOf describes r relative to the bounds b, or reports false when r
// overlaps b (e.g. it mirrors another display).
func placementOf(r, b displayRect) (x11Placement, bool) {
	switch {
	case r.X >= b.X+b.W:
		return x11Placement{Side: "right", Offset: r.Y - b.Y}, true
	case r.X+r.W <= b.X:
		return x11Placement{Side: "left", Offset: r.Y - b.Y}, true
	case r.Y >= b.Y+b.H:
		return x11Placement{Side: "below", Offset: r.X - b.X}, true
	case r.Y+r.H <= b.Y:
		return x11Placement{Side: "above", Offset: r.X - b.X}, true
	}
	return x11Placement{}, false
}

// placeAt puts a w x h display against the given side of b, keeping the
// offset within the shared edge so the two always touch.
func placeAt(p x11Placement, b displayRect, w, h int) displayRect {
	along := func(offset, size, edge int) int {
		return max(-(size - 1), min(offset, edge-1))
	}
	switch p.Side {
	case "left":
		return displayRect{X: b.X - w, Y: b.Y + along(p.Offset, h, b.H), W: w, H: h}
	case "below":
		return displayRect{X: b.X + along(p.Offset, w, b.W), Y: b.Y + b.H, W: w, H: h}
	case "above":
		return displayRect{X: b.X + along(p.Offset, w, b.W), Y: b.Y - h, W: w, H: h}
	default:
		return displayRect{X: b.X + b.W, Y: b.Y + along(p.Offset, h, b.H), W: w, H: h}
	}
}

// extendOutput is a display as the layout planners see it.
type extendOutput struct {
	Name   string
	Active bool
	Rect   displayRect
}

// planX11Layout decides where the w x h extended display goes (see
// planExtendLayout). An active output that already shows w x h is left
// alone, even when it mirrors another display.
func planX11Layout(st xrandrState, name string, w, h int, remembered *x11Placement) (x11Layout, error) {
	var cur xrandrOutput
	var outputs []extendOutput
	others := 0
	for _, o := range st.Outputs {
		outputs = append(outputs, extendOutput{Name: o.Name, Active: o.Active, Rect: o.Rect})
		switch {
		case o.Name == name:
			cur = o
		case o.Active:
			others++
		}
	}
	if others == 0 {
		return x11Layout{}, errors.New("no other active display to extend")
	}
	if cur.Active && cur.Rect.W == w && cur.Rect.H == h {
		return x11Layout{Keep: true, Rect: cur.Rect}, nil
	}
	layout := planExtendLayout(outputs, name, w, h, remembered, true)
	moved := map[string]x11OutputPos{}
	for _, m := range layout.Moves {
		moved[m.Name] = m
	}
	rects := []displayRect{layout.Rect}
	for _, o := range outputs {
		if o.Name == name || !o.Active {
			continue
		}
		r := o.Rect
		if m, ok := moved[o.Name]; ok {
			r.X, r.Y = m.X, m.Y
		}
		rects = append(rects, r)
	}
	all := boundsOf(rects)
	if (st.MaxW > 0 && all.X+all.W > st.MaxW) || (st.MaxH > 0 && all.Y+all.H > st.MaxH) {
		return x11Layout{}, fmt.Errorf("a %dx%d display does not fit in the X screen's %dx%d maximum", w, h, st.MaxW, st.MaxH)
	}
	return layout, nil
}

// planExtendLayout decides where the w x h extended display named name goes
// among outputs. An active display keeps its side of the desktop; an
// inactive one, or one overlapping the other displays, returns to where it
// was last switched off (remembered), or goes right of the other displays.
// anchored: the desktop keeps its origin at 0,0, so the other displays move
// when the extended display goes left of or above them.
func planExtendLayout(outputs []extendOutput, name string, w, h int, remembered *x11Placement, anchored bool) x11Layout {
	var cur extendOutput
	var others []extendOutput
	var rects []displayRect
	for _, o := range outputs {
		switch {
		case o.Name == name:
			cur = o
		case o.Active:
			others = append(others, o)
			rects = append(rects, o.Rect)
		}
	}
	if len(others) == 0 {
		return x11Layout{Rect: displayRect{W: w, H: h}}
	}
	b := boundsOf(rects)
	var r displayRect
	side, onSide := x11Placement{}, false
	if cur.Active {
		side, onSide = placementOf(cur.Rect, b)
	}
	switch {
	case onSide:
		r = placeAt(side, b, w, h)
	case remembered != nil:
		r = placeAt(*remembered, b, w, h)
	default:
		r = placeAt(x11Placement{Side: "right"}, b, w, h)
	}
	if !anchored {
		return x11Layout{Rect: r}
	}
	dx, dy := max(0, -min(r.X, b.X)), max(0, -min(r.Y, b.Y))
	layout := x11Layout{Rect: displayRect{X: r.X + dx, Y: r.Y + dy, W: w, H: h}}
	if dx != 0 || dy != 0 {
		for _, o := range others {
			layout.Moves = append(layout.Moves, x11OutputPos{Name: o.Name, X: o.Rect.X + dx, Y: o.Rect.Y + dy})
		}
	}
	return layout
}

func x11ApplyArgs(output, mode string, layout x11Layout) []string {
	args := []string{"--output", output, "--mode", mode, "--pos", fmt.Sprintf("%dx%d", layout.Rect.X, layout.Rect.Y)}
	for _, m := range layout.Moves {
		args = append(args, "--output", m.Name, "--pos", fmt.Sprintf("%dx%d", m.X, m.Y))
	}
	return args
}

// awaitX11Size polls until the output shows a w x h area, for two
// consecutive reads when more than one round is allowed.
func awaitX11Size(display, name string, w, h, rounds int) (xrandrState, bool, error) {
	var st xrandrState
	matches := 0
	for i := 0; i < rounds; i++ {
		if i > 0 {
			time.Sleep(x11SettlePoll)
		}
		var err error
		if st, err = queryXrandr(display); err != nil {
			return st, false, err
		}
		o, ok := st.output(name)
		if !ok || !o.Active || o.Rect.W != w || o.Rect.H != h {
			matches = 0
			continue
		}
		matches++
		if matches >= 2 || rounds == 1 {
			return st, true, nil
		}
	}
	return st, false, nil
}

func rectsOverlap(a, b displayRect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func x11ExtendedTarget(st xrandrState, o xrandrOutput) captureTarget {
	t := captureTarget{
		Kind: captureX11Grab,
		// Encoders need even dimensions; x11GrabCaptureArgs drops an odd
		// last row or column the same way.
		Width:         o.Rect.W &^ 1,
		Height:        o.Rect.H &^ 1,
		Rect:          o.Rect,
		DesktopWidth:  st.ScreenW,
		DesktopHeight: st.ScreenH,
	}
	for _, other := range st.Outputs {
		if other.Name != o.Name && other.Active && rectsOverlap(other.Rect, o.Rect) {
			t.Mirrored = true
		}
	}
	return t
}

type x11ConfigureResult struct {
	target captureTarget
	output string
	// changed: the host changed the output's mode list or layout, so the
	// output may be on even when configuring failed (Muffin and Mutter
	// answer a mode-list change by laying the screen out again, which can
	// switch the output on).
	changed bool
}

// configureX11Extended switches the virtual output on at w x h next to the
// other displays. name selects the output (empty: auto-detect). The caller
// holds x11Ext.mu.
func configureX11Extended(display, name string, w, h, nativeW, nativeH int) (x11ConfigureResult, error) {
	var res x11ConfigureResult
	hotplugged := false
	var lastErr error
	for attempt := 0; attempt < x11ApplyAttempts; attempt++ {
		st, err := queryXrandr(display)
		if err != nil {
			return res, err
		}
		out, err := pickX11VirtualOutput(st, name)
		if err != nil {
			return res, err
		}
		res.output, name = out.Name, out.Name
		mode, added, err := ensureX11Mode(display, out, w, h)
		if err != nil {
			return res, fmt.Errorf("adding a %dx%d mode to %s: %w", w, h, out.Name, err)
		}
		hotplugged = hotplugged || added
		if nativeW > 0 && nativeH > 0 && (nativeW != w || nativeH != h) {
			// Offered so it can be picked in the display settings.
			if _, added, err := ensureX11Mode(display, out, nativeW, nativeH); err != nil {
				log.Printf("X11 extended display: could not offer the tablet's native %dx%d mode: %v", nativeW, nativeH, err)
			} else {
				hotplugged = hotplugged || added
			}
		}
		res.changed = res.changed || hotplugged
		rounds := 1
		if hotplugged {
			rounds = x11SettleRounds
			time.Sleep(x11HotplugWait)
			if st, err = queryXrandr(display); err != nil {
				return res, err
			}
		}
		layout, err := planX11Layout(st, out.Name, w, h, x11Ext.placement[display+"/"+out.Name])
		if err != nil {
			return res, err
		}
		if !layout.Keep {
			res.changed = true
			if _, err := xrandrRun(display, x11ApplyArgs(out.Name, mode, layout)...); err != nil {
				lastErr = err
				continue
			}
			log.Printf("X11 extended display: switched %s on at %dx%d+%d+%d", out.Name, w, h, layout.Rect.X, layout.Rect.Y)
		}
		settled, ok, err := awaitX11Size(display, out.Name, w, h, rounds)
		if err != nil {
			return res, err
		}
		if ok {
			o, _ := settled.output(out.Name)
			res.target = x11ExtendedTarget(settled, o)
			return res, nil
		}
		lastErr = fmt.Errorf("%s did not keep the %dx%d mode (the window manager reconfigured the screen)", out.Name, w, h)
	}
	return res, lastErr
}

// switchOffX11Output turns the output off and moves the remaining displays
// back to the desktop origin. When the user had turned every other display
// off, it switches one back on rather than leave the screen without a
// visible display, and it hands the primary role on. It returns where the
// output was, so the next session can put it back there.
func switchOffX11Output(display, name string) (placement *x11Placement, switched bool, err error) {
	st, err := queryXrandr(display)
	if err != nil {
		return nil, false, err
	}
	o, ok := st.output(name)
	if !ok || !o.Active {
		return nil, false, nil
	}
	var others []xrandrOutput
	var rects []displayRect
	for _, other := range st.Outputs {
		if other.Name != name && other.Active {
			others = append(others, other)
			rects = append(rects, other.Rect)
		}
	}
	args := []string{"--output", name, "--off"}
	if len(others) > 0 {
		b := boundsOf(rects)
		if p, ok := placementOf(o.Rect, b); ok {
			placement = &p
		}
		for i, other := range others {
			var opts []string
			if b.X != 0 || b.Y != 0 {
				opts = append(opts, "--pos", fmt.Sprintf("%dx%d", other.Rect.X-b.X, other.Rect.Y-b.Y))
			}
			if o.Primary && i == 0 {
				opts = append(opts, "--primary")
			}
			if len(opts) > 0 {
				args = append(append(args, "--output", other.Name), opts...)
			}
		}
	} else if fallback, ok := x11FallbackOutput(st, name); ok {
		args = append(args, "--output", fallback.Name, "--auto", "--pos", "0x0")
		if o.Primary {
			args = append(args, "--primary")
		}
	}
	if _, err := xrandrRun(display, args...); err != nil {
		return nil, false, err
	}
	return placement, true, nil
}

// x11FallbackOutput picks the display to switch back on when the extended
// display is the only one on: the primary display, or else the first
// connected one that is not a virtual output.
func x11FallbackOutput(st xrandrState, except string) (xrandrOutput, bool) {
	var pick xrandrOutput
	found := false
	for _, o := range st.Outputs {
		if o.Name == except || !o.Connected || x11AutoOutputPattern.MatchString(o.Name) {
			continue
		}
		if o.Primary {
			return o, true
		}
		if !found {
			pick, found = o, true
		}
	}
	return pick, found
}

type x11EnabledOutput struct {
	display, output string
}

// x11Ext tracks the outputs the host switched on. mu also serializes every
// layout change the host makes.
var x11Ext = struct {
	mu sync.Mutex
	// gen identifies the latest session to claim the outputs; claimed is
	// whether that session still runs.
	gen     uint64
	claimed bool
	timer   *time.Timer
	enabled map[string]x11EnabledOutput // by display/output
	// placement remembers, per display/output, where the user had the
	// extended display when it was last switched off (process lifetime).
	placement map[string]*x11Placement
	released  map[string]*x11ReleasedOutput
	exiting   bool
}{
	enabled:   map[string]x11EnabledOutput{},
	released:  map[string]*x11ReleasedOutput{},
	placement: map[string]*x11Placement{},
}

func stopX11ReleaseTimerLocked() {
	if x11Ext.timer != nil {
		x11Ext.timer.Stop()
		x11Ext.timer = nil
	}
}

// releaseX11Locked ends session gen's claim and switches the outputs off
// after x11ReleaseGrace unless another session claims them first.
func releaseX11Locked(gen uint64) {
	if gen != x11Ext.gen {
		return
	}
	x11Ext.claimed = false
	scheduleX11SwitchOffLocked(gen, x11SwitchOffAttempts)
}

// scheduleX11SwitchOffLocked switches the enabled outputs off after
// x11ReleaseGrace, then keeps checking every x11SwitchOffRetry until each is
// confirmed off, up to attempts passes in all.
func scheduleX11SwitchOffLocked(gen uint64, attempts int) {
	scheduleX11SwitchOffAfterLocked(gen, attempts, x11ReleaseGrace)
}

func scheduleX11SwitchOffAfterLocked(gen uint64, attempts int, delay time.Duration) {
	stopX11ReleaseTimerLocked()
	if x11Ext.exiting || len(x11Ext.enabled)+len(x11Ext.released) == 0 {
		return
	}
	if attempts <= 0 {
		log.Printf("X11 extended display: giving up switching the output off; it is switched off when the host exits")
		return
	}
	x11Ext.timer = time.AfterFunc(delay, func() {
		x11Ext.mu.Lock()
		defer x11Ext.mu.Unlock()
		if gen != x11Ext.gen || x11Ext.claimed || x11Ext.exiting {
			return
		}
		x11Ext.timer = nil
		verifyX11ReleasedLocked()
		switchOffX11OutputsLocked()
		scheduleX11SwitchOffAfterLocked(gen, attempts-1, x11SwitchOffRetry)
	})
}

// switchOffX11OutputsLocked makes one pass over the enabled outputs: it
// switches off the ones that are on, and forgets the ones it finds off, so
// an output stays tracked until a later pass confirms it stayed off.
func switchOffX11OutputsLocked() {
	for key, e := range x11Ext.enabled {
		placement, _, err := switchOffX11Output(e.display, e.output)
		if err != nil {
			log.Printf("X11 extended display: could not switch %s off: %v", e.output, err)
			continue
		}
		if placement != nil {
			x11Ext.placement[key] = placement
		}
		delete(x11Ext.enabled, key)
		x11Ext.released[key] = &x11ReleasedOutput{x11EnabledOutput: e}
		log.Printf("X11 extended display: switched %s off", e.output)
	}
}

// x11VerifyPasses is how many consecutive passes must find a released output
// still off before it is no longer watched.
const x11VerifyPasses = 3

// x11ReleasedOutput is an output the host switched off and still watches,
// since a window manager can switch a connected output back on by itself.
type x11ReleasedOutput struct {
	x11EnabledOutput
	clean int
}

// verifyX11ReleasedLocked switches off again the released outputs that are
// on, and stops watching the ones found off x11VerifyPasses times in a row.
func verifyX11ReleasedLocked() {
	for key, r := range x11Ext.released {
		placement, switched, err := switchOffX11Output(r.display, r.output)
		switch {
		case err != nil:
			r.clean = 0
			log.Printf("X11 extended display: could not check that %s is off: %v", r.output, err)
		case switched:
			r.clean = 0
			if placement != nil {
				x11Ext.placement[key] = placement
			}
			log.Printf("X11 extended display: %s came back on after the session ended; switched it off again", r.output)
		default:
			r.clean++
			if r.clean >= x11VerifyPasses {
				delete(x11Ext.released, key)
			}
		}
	}
}

// releaseVirtualDisplaysOnExit switches off the X11 extended display and
// removes the GNOME virtual monitor, which would otherwise stay on as an
// empty monitor, or leave the other displays as GNOME lays them out, after
// the host exits.
func releaseVirtualDisplaysOnExit() {
	x11Ext.mu.Lock()
	x11Ext.exiting = true
	stopX11ReleaseTimerLocked()
	verifyX11ReleasedLocked()
	switchOffX11OutputsLocked()
	x11Ext.mu.Unlock()
	releaseMutterOnExit()
	releaseWlExtOnExit()
}

// x11ExtendedDisplay is one session's use of the virtual output.
type x11ExtendedDisplay struct {
	display          string
	output           string
	gen              uint64
	nativeW, nativeH int
	tabletMapped     bool // see captureTarget.TabletMapped

	mu         sync.Mutex
	target     captureTarget
	reqW, reqH int // configured resolution

	stopWatch chan struct{}
	watchDone chan struct{}
	closeOnce sync.Once
}

// newX11ExtendedDisplay switches the virtual output on for a new session at
// the configured resolution.
func newX11ExtendedDisplay(opts videoOptions, display string) (*x11ExtendedDisplay, error) {
	w, h := opts.Width, opts.Height
	if w <= 0 || h <= 0 {
		w, h = x11DefaultWidth, x11DefaultHeight
	}
	ident := activeClientIdentity()

	x11Ext.mu.Lock()
	defer x11Ext.mu.Unlock()
	if x11Ext.exiting {
		return nil, errors.New("host is shutting down")
	}
	x11Ext.gen++
	gen := x11Ext.gen
	x11Ext.claimed = true
	stopX11ReleaseTimerLocked()
	clear(x11Ext.released)

	res, err := configureX11Extended(display, strings.TrimSpace(os.Getenv(x11OutputEnv)), w, h, ident.NativeWidth, ident.NativeHeight)
	if res.output != "" && (err == nil || res.changed) {
		x11Ext.enabled[display+"/"+res.output] = x11EnabledOutput{display: display, output: res.output}
	}
	if err != nil {
		// Switch the output off now rather than after the grace period: the
		// session mirrors the X screen instead, which should not include a
		// display nobody sees.
		switchOffX11OutputsLocked()
		releaseX11Locked(gen) // keeps checking that it stays off
		return nil, err
	}
	d := &x11ExtendedDisplay{
		display:      display,
		output:       res.output,
		gen:          gen,
		nativeW:      ident.NativeWidth,
		nativeH:      ident.NativeHeight,
		tabletMapped: mapX11Tablet(res.output),
		reqW:         w,
		reqH:         h,
		stopWatch:    make(chan struct{}),
		watchDone:    make(chan struct{}),
	}
	d.setTarget(res.target)
	go d.watch()
	return d, nil
}

func (d *x11ExtendedDisplay) Target() captureTarget {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.target
}

func (d *x11ExtendedDisplay) setTarget(t captureTarget) {
	t.TabletMapped = d.tabletMapped
	d.mu.Lock()
	d.target = t
	d.mu.Unlock()
}

func (d *x11ExtendedDisplay) Description() string {
	t := d.Target()
	return fmt.Sprintf("X11 extended display %s (%dx%d at +%d+%d)", d.output, t.Rect.W, t.Rect.H, t.Rect.X, t.Rect.Y)
}

// Refresh re-reads the output's place on the X screen, which the user can
// change in the display settings at any time.
func (d *x11ExtendedDisplay) Refresh() (captureTarget, error) {
	st, err := queryXrandr(d.display)
	if err != nil {
		return captureTarget{}, err
	}
	o, ok := st.output(d.output)
	if !ok {
		return captureTarget{}, fmt.Errorf("RandR output %s is gone", d.output)
	}
	if !o.Active {
		return captureTarget{}, fmt.Errorf("RandR output %s is switched off", d.output)
	}
	t := x11ExtendedTarget(st, o)
	d.setTarget(t)
	return d.Target(), nil
}

// Recover switches the output back on after repeated capture failures.
func (d *x11ExtendedDisplay) Recover() error {
	d.mu.Lock()
	w, h := d.reqW, d.reqH
	d.mu.Unlock()
	x11Ext.mu.Lock()
	defer x11Ext.mu.Unlock()
	if x11Ext.exiting {
		return errors.New("host is shutting down")
	}
	res, err := configureX11Extended(d.display, d.output, w, h, d.nativeW, d.nativeH)
	if res.changed {
		x11Ext.enabled[d.display+"/"+d.output] = x11EnabledOutput{display: d.display, output: d.output}
	}
	if err != nil {
		return err
	}
	d.setTarget(res.target)
	return nil
}

func (d *x11ExtendedDisplay) Close() {
	d.closeOnce.Do(func() {
		close(d.stopWatch)
		<-d.watchDone
		x11Ext.mu.Lock()
		releaseX11Locked(d.gen)
		x11Ext.mu.Unlock()
	})
}

// x11Observed is the output's state as one watcher poll saw it.
type x11Observed struct {
	Active             bool
	Rect               displayRect
	DesktopW, DesktopH int
	// AutoMode: the output shows its preferred mode, which the window
	// manager picks by itself on a hotplug.
	AutoMode bool
	// Outputs lists the connected outputs and their modes; the window
	// manager lays the screen out again when they change (a hotplug).
	Outputs string
}

func observeX11Output(st xrandrState, name string) x11Observed {
	var outputs strings.Builder
	for _, o := range st.Outputs {
		if !o.Connected {
			continue
		}
		outputs.WriteString(o.Name)
		for _, m := range o.Modes {
			outputs.WriteString(" " + m.Name)
		}
		outputs.WriteString(";")
	}
	obs := x11Observed{DesktopW: st.ScreenW, DesktopH: st.ScreenH, Outputs: outputs.String()}
	o, ok := st.output(name)
	if !ok {
		return obs
	}
	obs.Active, obs.Rect = o.Active, o.Rect
	for _, m := range o.Modes {
		if m.Current && m.Preferred {
			obs.AutoMode = true
		}
	}
	return obs
}

// watch follows the output while the session runs, like the GNOME virtual
// monitor's watcher: a resolution picked in the display settings is saved as
// the configured one, and any change to the output's place on the X screen
// restarts the session so capture and input mapping follow. A state must be
// seen on two consecutive polls, so the window manager's intermediate steps
// are ignored.
func (d *x11ExtendedDisplay) watch() {
	defer close(d.watchDone)
	ticker := time.NewTicker(x11WatchInterval)
	defer ticker.Stop()
	// base is the last state handled; nil until the session's first one.
	var last, base *x11Observed
	for {
		select {
		case <-d.stopWatch:
			return
		case <-ticker.C:
		}
		st, err := queryXrandr(d.display)
		if err != nil {
			last = nil
			continue
		}
		obs := observeX11Output(st, d.output)
		stable := last != nil && *last == obs
		last = &obs
		if !stable {
			continue
		}
		// The window manager switches the output to its preferred mode by
		// itself after a hotplug: the mode-list change that started the
		// session, or a monitor being plugged in or out. Otherwise the
		// preferred mode, too, was picked by the user.
		hotplug := base == nil || base.Outputs != obs.Outputs
		d.saveUserResolution(obs, obs.AutoMode && hotplug)
		base = &obs
		t := d.Target()
		if obs.Active && obs.Rect == t.Rect && obs.DesktopW == t.DesktopWidth && obs.DesktopH == t.DesktopHeight {
			continue
		}
		if obs.Active {
			log.Printf("X11 extended display %s is now %dx%d+%d+%d on a %dx%d screen; restarting video session",
				d.output, obs.Rect.W, obs.Rect.H, obs.Rect.X, obs.Rect.Y, obs.DesktopW, obs.DesktopH)
		} else {
			log.Printf("X11 extended display %s was switched off; restarting video session", d.output)
		}
		restartActiveVideoSession()
		return
	}
}

// saveUserResolution saves the output's size as the configured resolution
// when the user picked a different one, so the next session (and the
// restart that follows the change) keeps it. auto: the window manager picked
// the size by itself, so it is not saved.
func (d *x11ExtendedDisplay) saveUserResolution(obs x11Observed, auto bool) {
	d.mu.Lock()
	reqW, reqH := d.reqW, d.reqH
	d.mu.Unlock()
	w, h := obs.Rect.W, obs.Rect.H
	if !obs.Active || (w == reqW && h == reqH) || auto {
		return
	}
	d.mu.Lock()
	d.reqW, d.reqH = w, h
	d.mu.Unlock()
	if globalSettingsStore == nil {
		return
	}
	if err := globalSettingsStore.updateResolution(w, h); err != nil {
		log.Printf("Could not save the X11 extended display resolution %dx%d: %v", w, h, err)
		return
	}
	log.Printf("X11 extended display resolution changed in the display settings (%dx%d -> %dx%d); saved as the configured resolution", reqW, reqH, w, h)
}
