//go:build linux

package main

// GNOME Wayland extended display: a Mutter ScreenCast virtual monitor.
//
// Mutter can create a monitor on request. For each video session the host:
//
//  1. calls org.gnome.Mutter.ScreenCast CreateSession, Session.RecordVirtual
//     and Session.Start. Mutter plugs a virtual monitor into the desktop
//     that offers the standard resolutions, the configured one (preferred)
//     and the tablet's native one, and announces the PipeWire node it renders
//     the monitor into (pipeline_linux.go records it with pipewiresrc);
//  2. arranges the displays with DisplayConfig.ApplyMonitorsConfig: the
//     virtual monitor at the configured resolution next to the other
//     displays, and the other displays back as they were. Mutter has no
//     saved configuration for a set of monitors that includes a virtual
//     monitor (each one gets a new serial), so it lays the whole desktop out
//     again in a row at default settings when the monitor appears;
//  3. points the Drawsplay Pen's "Map to Monitor" setting at the monitor, so
//     touch/pen input lands on it (x11_tablet_linux.go).
//
// From there on it behaves like the X11 extended display
// (x11_extend_linux.go): resolution and position are OS-managed (Settings ->
// Displays); a watcher saves a resolution picked there as the configured one
// and restarts the session for it; the monitor stays across a quick
// reconnect (mutterReleaseGrace) and comes back where the user last had it;
// and when it goes, the other displays stay as the user arranged them.
//
// Differences that come from Mutter:
//   - Capture follows the monitor itself, so moving it needs no restart.
//   - Stopping the screen share from GNOME's indicator removes the monitor.
//     The session then restarts with a new one.
//   - Mutter leaves the mouse pointer out of a virtual monitor's stream
//     while a physical display uses a hardware cursor; a second, 1 fps
//     stream of the virtual monitor turns that off while the pointer is on
//     it (mutter_cursor_linux.go).
//   - Mutter streams a rotated virtual monitor at its unrotated size, so
//     rotating it is not supported.
//   - The monitor must not be disabled in Settings -> Displays: GNOME Shell
//     50 crashes on a configuration that leaves a virtual monitor out, so
//     the host never applies one.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	mutterBusName      = "org.gnome.Mutter.ScreenCast"
	mutterObjectPath   = "/org/gnome/Mutter/ScreenCast"
	mutterIface        = "org.gnome.Mutter.ScreenCast"
	mutterSessionIface = "org.gnome.Mutter.ScreenCast.Session"
	mutterStreamIface  = "org.gnome.Mutter.ScreenCast.Stream"

	// Mutter's DisplayConfig interface lists, arranges and watches the
	// monitors.
	displayConfigBusName    = "org.gnome.Mutter.DisplayConfig"
	displayConfigObjectPath = "/org/gnome/Mutter/DisplayConfig"
	displayConfigIface      = "org.gnome.Mutter.DisplayConfig"

	pipewireNodeTimeout = 10 * time.Second
	mutterCallTimeout   = 5 * time.Second

	// mutterApplyTemporary applies a configuration without saving it to
	// monitors.xml or asking the user to keep it.
	mutterApplyTemporary = 1
	// Layout modes: monitor positions and sizes in logical pixels (the mode
	// size divided by the scale) or in physical ones.
	mutterLayoutLogical  = 1
	mutterLayoutPhysical = 2

	// mutterApplyAttempts bounds retries when the monitors change between
	// reading their state and applying a layout.
	mutterApplyAttempts = 3
)

// Timing knobs; variables so tests run without real waits.
var (
	// mutterReleaseGrace keeps the virtual monitor across a quick reconnect.
	mutterReleaseGrace = 5 * time.Second
	// mutterSettle coalesces the burst of MonitorsChanged signals one change
	// in the display settings causes.
	mutterSettle = 400 * time.Millisecond
	// Mutter lists a new monitor shortly after the stream announces it, and
	// drops a removed one shortly after the session stops.
	mutterLookupAttempts = 10
	mutterLookupDelay    = 150 * time.Millisecond
	// mutterPrimeAfter is how many lookups find no monitor before the host
	// asks for one with a PipeWire consumer (see awaitNewMutterMonitor);
	// Mutter 50 lists its monitor before announcing the stream.
	// mutterPrimeAttempts bounds the lookups after that: the consumer has
	// to start and agree a format first.
	mutterPrimeAfter    = 2
	mutterPrimeAttempts = 30
)

var errMutterModeMissing = errors.New("the virtual monitor does not offer")

// virtualMonitorResolutionPresets are the standard resolutions the virtual
// monitor offers in Settings -> Displays (the set the web UI's resolution
// dropdown used to offer).
var virtualMonitorResolutionPresets = []struct{ Width, Height int }{
	{1280, 720},
	{1600, 900},
	{1680, 1050},
	{1920, 1080},
	{2560, 1440},
	{2736, 1824},
	{2880, 1800},
	{3200, 1800},
	{3840, 2160},
}

// virtualMonitorModeSize is RecordVirtual's mode "size" property, D-Bus
// signature "(uu)".
type virtualMonitorModeSize struct {
	Width  uint32
	Height uint32
}

// virtualMonitorModes builds RecordVirtual's "modes" property: the standard
// resolutions, the configured one and the tablet's native one, so each can
// be picked in Settings -> Displays. The configured one is preferred, which
// makes Mutter start the monitor at it.
func virtualMonitorModes(w, h, nativeW, nativeH int) []map[string]dbus.Variant {
	sizes := append([]struct{ Width, Height int }{}, virtualMonitorResolutionPresets...)
	sizes = append(sizes, struct{ Width, Height int }{w, h}, struct{ Width, Height int }{nativeW, nativeH})
	offered := map[[2]int]bool{}
	var modes []map[string]dbus.Variant
	for _, s := range sizes {
		if s.Width <= 0 || s.Height <= 0 || offered[[2]int{s.Width, s.Height}] {
			continue
		}
		offered[[2]int{s.Width, s.Height}] = true
		mode := map[string]dbus.Variant{
			"size": dbus.MakeVariant(virtualMonitorModeSize{uint32(s.Width), uint32(s.Height)}),
		}
		if s.Width == w && s.Height == h {
			mode["is-preferred"] = dbus.MakeVariant(true)
		}
		modes = append(modes, mode)
	}
	return modes
}

// --- monitor state ----------------------------------------------------------

type mutterMode struct {
	ID             string
	W, H           int
	PreferredScale float64
	Scales         []float64
	Current        bool
	Preferred      bool
}

// supports returns Mutter's own value for scale when the mode supports it:
// Mutter accepts a scale only when it matches a supported one.
func (m mutterMode) supports(scale float64) (float64, bool) {
	for _, s := range m.Scales {
		if math.Abs(s-scale) < 1e-6 {
			return s, true
		}
	}
	return 0, false
}

type mutterMonitor struct {
	Spec  monitorSpec
	Modes []mutterMode
	// Props are the monitor settings a configuration resets unless it
	// passes them back (underscanning, color mode, RGB range), keyed as
	// ApplyMonitorsConfig takes them.
	Props    map[string]dbus.Variant
	ForLease bool
}

func (m mutterMonitor) currentMode() (mutterMode, bool) {
	for _, mode := range m.Modes {
		if mode.Current {
			return mode, true
		}
	}
	return mutterMode{}, false
}

func (m mutterMonitor) modeByID(id string) (mutterMode, bool) {
	for _, mode := range m.Modes {
		if mode.ID == id {
			return mode, true
		}
	}
	return mutterMode{}, false
}

// mode returns a w x h mode, preferring the current one, then the preferred
// one (a monitor can offer a size at several refresh rates).
func (m mutterMonitor) mode(w, h int) (mutterMode, bool) {
	var pick mutterMode
	found := false
	for _, mode := range m.Modes {
		if mode.W != w || mode.H != h {
			continue
		}
		if mode.Current {
			return mode, true
		}
		if !found || (mode.Preferred && !pick.Preferred) {
			pick, found = mode, true
		}
	}
	return pick, found
}

type mutterLogicalMonitor struct {
	X, Y      int
	Scale     float64
	Transform uint32
	Primary   bool
	// Monitors shows the same picture; more than one when mirroring.
	Monitors []monitorSpec
}

// mutterState is DisplayConfig.GetCurrentState's answer.
type mutterState struct {
	Serial              uint32
	Monitors            []mutterMonitor
	Logical             []mutterLogicalMonitor
	LayoutMode          uint32
	CanChangeLayoutMode bool
	GlobalScale         bool
}

func (st mutterState) monitor(connector string) (mutterMonitor, bool) {
	for _, m := range st.Monitors {
		if m.Spec.Connector == connector {
			return m, true
		}
	}
	return mutterMonitor{}, false
}

// logicalIndex returns the logical monitor showing connector, or -1 when the
// monitor is disabled.
func (st mutterState) logicalIndex(connector string) int {
	for i, lm := range st.Logical {
		for _, m := range lm.Monitors {
			if m.Connector == connector {
				return i
			}
		}
	}
	return -1
}

// rect is the logical monitor's area of the desktop.
func (st mutterState) rect(lm mutterLogicalMonitor) displayRect {
	r := displayRect{X: lm.X, Y: lm.Y}
	if len(lm.Monitors) == 0 {
		return r
	}
	m, ok := st.monitor(lm.Monitors[0].Connector)
	if !ok {
		return r
	}
	if mode, ok := m.currentMode(); ok {
		r.W, r.H = mutterLogicalSize(mode.W, mode.H, lm.Scale, lm.Transform, st.LayoutMode)
	}
	return r
}

// mutterLogicalSize is the size a monitor showing a w x h mode takes up in
// the desktop: turned by the transform, and in the logical layout mode
// divided by the scale, rounded in single precision as Mutter does.
func mutterLogicalSize(w, h int, scale float64, transform, layoutMode uint32) (int, int) {
	if transform%2 == 1 {
		w, h = h, w
	}
	if layoutMode == mutterLayoutLogical && scale > 0 {
		w = int(math.Round(float64(float32(w) / float32(scale))))
		h = int(math.Round(float64(float32(h) / float32(scale))))
	}
	return w, h
}

// parseMutterState decodes GetCurrentState's reply, signature
// (ua((ssss)a(siiddada{sv})a{sv})a(iiduba(ssss)a{sv})a{sv}).
func parseMutterState(body []interface{}) (mutterState, error) {
	var serial uint32
	var monitors []struct {
		Spec  monitorSpec
		Modes []struct {
			ID                      string
			W, H                    int32
			Refresh, PreferredScale float64
			Scales                  []float64
			Props                   map[string]dbus.Variant
		}
		Props map[string]dbus.Variant
	}
	var logical []struct {
		X, Y      int32
		Scale     float64
		Transform uint32
		Primary   bool
		Monitors  []monitorSpec
		Props     map[string]dbus.Variant
	}
	var props map[string]dbus.Variant
	if err := dbus.Store(body, &serial, &monitors, &logical, &props); err != nil {
		return mutterState{}, fmt.Errorf("GetCurrentState: %w", err)
	}
	st := mutterState{Serial: serial, LayoutMode: mutterLayoutPhysical}
	if v, ok := props["layout-mode"].Value().(uint32); ok {
		st.LayoutMode = v
	}
	st.CanChangeLayoutMode, _ = props["supports-changing-layout-mode"].Value().(bool)
	st.GlobalScale, _ = props["global-scale-required"].Value().(bool)
	for _, m := range monitors {
		mon := mutterMonitor{Spec: m.Spec, Props: map[string]dbus.Variant{}}
		for _, mode := range m.Modes {
			current, _ := mode.Props["is-current"].Value().(bool)
			preferred, _ := mode.Props["is-preferred"].Value().(bool)
			mon.Modes = append(mon.Modes, mutterMode{
				ID: mode.ID, W: int(mode.W), H: int(mode.H),
				PreferredScale: mode.PreferredScale, Scales: mode.Scales,
				Current: current, Preferred: preferred,
			})
		}
		if v, ok := m.Props["is-underscanning"]; ok {
			mon.Props["underscanning"] = v
		}
		for _, key := range []string{"color-mode", "rgb-range"} {
			if v, ok := m.Props[key]; ok {
				mon.Props[key] = v
			}
		}
		mon.ForLease, _ = m.Props["is-for-lease"].Value().(bool)
		st.Monitors = append(st.Monitors, mon)
	}
	for _, lm := range logical {
		st.Logical = append(st.Logical, mutterLogicalMonitor{
			X: int(lm.X), Y: int(lm.Y), Scale: lm.Scale, Transform: lm.Transform,
			Primary: lm.Primary, Monitors: lm.Monitors,
		})
	}
	return st, nil
}

// mutterLogicalConfig and mutterMonitorConfig are ApplyMonitorsConfig's
// logical monitor argument, signature a(iiduba(ssa{sv})).
type mutterLogicalConfig struct {
	X, Y      int32
	Scale     float64
	Transform uint32
	Primary   bool
	Monitors  []mutterMonitorConfig
}

type mutterMonitorConfig struct {
	Connector string
	ModeID    string
	Props     map[string]dbus.Variant
}

// --- layout -------------------------------------------------------------------

type mutterLayout struct {
	// Keep: the displays are already arranged as planned.
	Keep bool
	Rect displayRect // the virtual monitor's area of the desktop
	// Configs and Props are the ApplyMonitorsConfig arguments.
	Configs []mutterLogicalConfig
	Props   map[string]dbus.Variant
	// Others identifies the other monitors when their arrangement was taken
	// from before the virtual monitor appeared (mutterOthersSignature).
	Others string
}

// planMutterLayout decides where the virtual monitor (connector) goes at
// w x h, like planX11Layout: a monitor that already shows w x h is left
// alone, even when it mirrors another display; one on a side of the desktop
// keeps that side; otherwise it returns to where it was when last removed
// (remembered), or goes right of the other displays.
//
// before is the state from just before Mutter added the monitor, or nil for
// a monitor that has been in use. The other displays are put back as before
// shows them, provided it lists the same ones.
//
// Unlike X, Mutter requires every display to touch another one, so the
// monitor is slid along its side until it does.
func planMutterLayout(st mutterState, connector string, w, h int, remembered *x11Placement, before *mutterState) (mutterLayout, error) {
	mon, ok := st.monitor(connector)
	if !ok {
		return mutterLayout{}, fmt.Errorf("%s is not listed by Mutter", connector)
	}
	mode, ok := mon.mode(w, h)
	if !ok {
		return mutterLayout{}, fmt.Errorf("%w %dx%d", errMutterModeMissing, w, h)
	}
	cur := st.logicalIndex(connector)
	if before == nil && cur >= 0 {
		if m, ok := mon.currentMode(); ok && m.W == w && m.H == h {
			return mutterLayout{Keep: true, Rect: st.rect(st.Logical[cur])}, nil
		}
	}

	var layout mutterLayout
	src, fromBefore := st, false
	if before != nil && mutterBeforeUsable(*before, st, connector) {
		src, fromBefore = *before, true
		layout.Others = mutterOthersSignature(st, connector)
	}
	others := mutterOtherLogical(src, connector)
	rects := make([]displayRect, len(others))
	for i, lm := range others {
		rects[i] = src.rect(lm)
	}

	transform, scale, primary := uint32(0), 0.0, false
	alone := false
	if cur >= 0 {
		lm := st.Logical[cur]
		transform, scale = lm.Transform, lm.Scale
		alone = len(lm.Monitors) == 1
		primary = lm.Primary && alone && !fromBefore
	}
	scale = mutterPickScale(mode, scale, others, st.GlobalScale, src.LayoutMode)
	lw, lh := mutterLogicalSize(w, h, scale, transform, src.LayoutMode)

	var r displayRect
	if len(others) == 0 {
		r, primary = displayRect{W: lw, H: lh}, true
	} else {
		b := boundsOf(rects)
		side := x11Placement{Side: "right"}
		onSide := false
		if before == nil && cur >= 0 && alone {
			side, onSide = placementOf(st.rect(st.Logical[cur]), b)
		}
		if !onSide {
			side = x11Placement{Side: "right"}
			if remembered != nil {
				side = *remembered
			}
		}
		r = adjoinMutterRect(placeAt(side, b, lw, lh), side.Side, b, rects)
		if !mutterLayoutValid(append(append([]displayRect{}, rects...), r)) && cur >= 0 && alone {
			// The other displays only touch through the monitor (it sits
			// between them): change its size where it is instead.
			cr := st.rect(st.Logical[cur])
			r = displayRect{X: cr.X, Y: cr.Y, W: lw, H: lh}
		}
		if !mutterLayoutValid(append(append([]displayRect{}, rects...), r)) {
			return mutterLayout{}, fmt.Errorf("no place for a %dx%d monitor that touches the other displays", w, h)
		}
		dx, dy := -min(r.X, b.X), -min(r.Y, b.Y)
		r.X, r.Y = r.X+dx, r.Y+dy
		for i := range others {
			others[i].X, others[i].Y = others[i].X+dx, others[i].Y+dy
		}
	}
	layout.Rect = r

	if primary {
		for i := range others {
			others[i].Primary = false
		}
	} else if !mutterHasPrimary(others) {
		others[0].Primary = true
	}
	configs, err := mutterConfigs(src, others)
	if err != nil {
		return mutterLayout{}, err
	}
	layout.Configs = append(configs, mutterLogicalConfig{
		X: int32(r.X), Y: int32(r.Y), Scale: scale, Transform: transform, Primary: primary,
		Monitors: []mutterMonitorConfig{{Connector: connector, ModeID: mode.ID, Props: mon.Props}},
	})
	layout.Props = mutterApplyProps(st, src)
	layout.Keep = mutterShows(st, src, layout.Configs)
	return layout, nil
}

// mutterBeforeUsable reports whether before lists the same monitors as st
// apart from the virtual monitor, with modes they still offer, so their
// arrangement can be restored from it. Mutter lays the desktop out again in
// its default layout mode, so before's can differ only where the layout
// mode can be set back.
func mutterBeforeUsable(before, st mutterState, connector string) bool {
	if (before.LayoutMode != st.LayoutMode && !st.CanChangeLayoutMode) || mutterOthersSignature(before, connector) != mutterOthersSignature(st, connector) {
		return false
	}
	for _, lm := range before.Logical {
		for _, spec := range lm.Monitors {
			bm, ok := before.monitor(spec.Connector)
			if !ok || spec.Connector == connector {
				return false
			}
			mode, ok := bm.currentMode()
			if !ok {
				return false
			}
			m, _ := st.monitor(spec.Connector)
			if _, ok := m.modeByID(mode.ID); !ok {
				return false
			}
		}
	}
	return true
}

// mutterOthersSignature identifies the monitors other than connector.
func mutterOthersSignature(st mutterState, connector string) string {
	var specs []string
	for _, m := range st.Monitors {
		if m.Spec.Connector != connector {
			specs = append(specs, strings.Join([]string{m.Spec.Connector, m.Spec.Vendor, m.Spec.Product, m.Spec.Serial}, "\x1f"))
		}
	}
	sort.Strings(specs)
	return strings.Join(specs, "\x1e")
}

// mutterOtherLogical returns st's logical monitors without connector.
func mutterOtherLogical(st mutterState, connector string) []mutterLogicalMonitor {
	var others []mutterLogicalMonitor
	for _, lm := range st.Logical {
		var members []monitorSpec
		for _, m := range lm.Monitors {
			if m.Connector != connector {
				members = append(members, m)
			}
		}
		if len(members) > 0 {
			lm.Monitors = members
			others = append(others, lm)
		}
	}
	return others
}

// mutterPickScale keeps the monitor's scale when the mode supports it. A
// new monitor gets Mutter's preferred scale for the mode; one sharing a
// global scale gets the other displays' scale. The physical layout mode
// takes whole scales only.
func mutterPickScale(mode mutterMode, current float64, others []mutterLogicalMonitor, global bool, layoutMode uint32) float64 {
	candidates := []float64{current, mode.PreferredScale, 1}
	if global && len(others) > 0 {
		candidates = append([]float64{others[0].Scale}, candidates...)
	}
	for _, c := range candidates {
		if s, ok := mode.supports(c); ok && (layoutMode != mutterLayoutPhysical || s == math.Trunc(s)) {
			return s
		}
	}
	if len(mode.Scales) > 0 {
		return mode.Scales[0]
	}
	return 1
}

// adjoinMutterRect slides r, put against the given side of the bounds b,
// along that side to the nearest position where it shares part of an edge
// with one of the displays (rects) on that side.
func adjoinMutterRect(r displayRect, side string, b displayRect, rects []displayRect) displayRect {
	best, bestDist, found := 0, 0, false
	for _, o := range rects {
		var lo, hi, at int
		switch side {
		case "left":
			if o.X != b.X {
				continue
			}
			lo, hi, at = o.Y-r.H+1, o.Y+o.H-1, r.Y
		case "below":
			if o.Y+o.H != b.Y+b.H {
				continue
			}
			lo, hi, at = o.X-r.W+1, o.X+o.W-1, r.X
		case "above":
			if o.Y != b.Y {
				continue
			}
			lo, hi, at = o.X-r.W+1, o.X+o.W-1, r.X
		default:
			if o.X+o.W != b.X+b.W {
				continue
			}
			lo, hi, at = o.Y-r.H+1, o.Y+o.H-1, r.Y
		}
		pos := min(max(at, lo), hi)
		dist := pos - at
		if dist < 0 {
			dist = -dist
		}
		if !found || dist < bestDist {
			best, bestDist, found = pos, dist, true
		}
	}
	if !found {
		return r
	}
	if side == "below" || side == "above" {
		r.X = best
	} else {
		r.Y = best
	}
	return r
}

// rectsAdjacent reports whether a and b share part of an edge.
func rectsAdjacent(a, b displayRect) bool {
	if (a.X == b.X+b.W || a.X+a.W == b.X) && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
		return true
	}
	return (a.Y == b.Y+b.H || a.Y+a.H == b.Y) && a.X < b.X+b.W && b.X < a.X+a.W
}

// mutterLayoutValid reports whether Mutter accepts displays at rects: none
// overlap and each touches the others through shared edges.
func mutterLayoutValid(rects []displayRect) bool {
	for i := range rects {
		for j := i + 1; j < len(rects); j++ {
			if rectsOverlap(rects[i], rects[j]) {
				return false
			}
		}
	}
	if len(rects) <= 1 {
		return true
	}
	reached := make([]bool, len(rects))
	reached[0] = true
	stack, n := []int{0}, 1
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for j := range rects {
			if !reached[j] && rectsAdjacent(rects[i], rects[j]) {
				reached[j] = true
				n++
				stack = append(stack, j)
			}
		}
	}
	return n == len(rects)
}

func mutterHasPrimary(lms []mutterLogicalMonitor) bool {
	for _, lm := range lms {
		if lm.Primary {
			return true
		}
	}
	return false
}

// mutterConfigs turns logical monitors of st into ApplyMonitorsConfig
// arguments, keeping each monitor's mode and settings.
func mutterConfigs(st mutterState, lms []mutterLogicalMonitor) ([]mutterLogicalConfig, error) {
	var configs []mutterLogicalConfig
	for _, lm := range lms {
		cfg := mutterLogicalConfig{X: int32(lm.X), Y: int32(lm.Y), Scale: lm.Scale, Transform: lm.Transform, Primary: lm.Primary}
		for _, spec := range lm.Monitors {
			m, ok := st.monitor(spec.Connector)
			if !ok {
				return nil, fmt.Errorf("%s is not listed by Mutter", spec.Connector)
			}
			mode, ok := m.currentMode()
			if !ok {
				return nil, fmt.Errorf("%s has no current mode", spec.Connector)
			}
			cfg.Monitors = append(cfg.Monitors, mutterMonitorConfig{Connector: spec.Connector, ModeID: mode.ID, Props: m.Props})
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

// mutterApplyProps are the configuration-wide ApplyMonitorsConfig
// properties: the layout mode, which Mutter otherwise resets to its default,
// and the monitors leased out (e.g. to a VR headset), which it otherwise
// takes back. Both are kept as src, the state the arrangement comes from,
// has them.
func mutterApplyProps(st, src mutterState) map[string]dbus.Variant {
	props := map[string]dbus.Variant{}
	if st.CanChangeLayoutMode {
		props["layout-mode"] = dbus.MakeVariant(src.LayoutMode)
	}
	var leased []monitorSpec
	for _, m := range src.Monitors {
		if m.ForLease {
			leased = append(leased, m.Spec)
		}
	}
	if len(leased) > 0 {
		props["monitors-for-lease"] = dbus.MakeVariant(leased)
	}
	return props
}

// mutterShows reports whether st already shows configs as planned from src:
// arranged so, in src's layout mode, with the monitors src leases out.
func mutterShows(st, src mutterState, configs []mutterLogicalConfig) bool {
	return st.LayoutMode == src.LayoutMode && mutterLeased(st) == mutterLeased(src) && sameMutterLayout(st, configs)
}

// mutterLeased lists the monitors st leases out.
func mutterLeased(st mutterState) string {
	var leased []string
	for _, m := range st.Monitors {
		if m.ForLease {
			leased = append(leased, m.Spec.Connector)
		}
	}
	sort.Strings(leased)
	return strings.Join(leased, " ")
}

// sameMutterLayout reports whether st already shows configs.
func sameMutterLayout(st mutterState, configs []mutterLogicalConfig) bool {
	describe := func(x, y int, scale float64, transform uint32, primary bool, monitors []string) string {
		sort.Strings(monitors)
		return fmt.Sprintf("%d,%d,%.6f,%d,%t,%s", x, y, scale, transform, primary, strings.Join(monitors, " "))
	}
	var have, want []string
	for _, lm := range st.Logical {
		var monitors []string
		for _, spec := range lm.Monitors {
			m, _ := st.monitor(spec.Connector)
			mode, _ := m.currentMode()
			monitors = append(monitors, spec.Connector+"="+mode.ID)
		}
		have = append(have, describe(lm.X, lm.Y, lm.Scale, lm.Transform, lm.Primary, monitors))
	}
	for _, c := range configs {
		var monitors []string
		for _, m := range c.Monitors {
			monitors = append(monitors, m.Connector+"="+m.ModeID)
		}
		want = append(want, describe(int(c.X), int(c.Y), c.Scale, c.Transform, c.Primary, monitors))
	}
	sort.Strings(have)
	sort.Strings(want)
	return strings.Join(have, ";") == strings.Join(want, ";")
}

// planMutterRelease plans the desktop without the virtual monitor, like
// switchOffX11Output: the other displays stay where they are, moved back to
// the desktop origin, and the first takes over the primary role if the
// virtual monitor had it. nil when no display remains or the remaining ones
// would not touch each other (Mutter then arranges them by itself).
func planMutterRelease(st mutterState, connector string) []mutterLogicalConfig {
	others := mutterOtherLogical(st, connector)
	if len(others) == 0 {
		return nil
	}
	rects := make([]displayRect, len(others))
	for i, lm := range others {
		rects[i] = st.rect(lm)
	}
	if !mutterLayoutValid(rects) {
		return nil
	}
	b := boundsOf(rects)
	for i := range others {
		others[i].X -= b.X
		others[i].Y -= b.Y
	}
	if !mutterHasPrimary(others) {
		others[0].Primary = true
	}
	configs, err := mutterConfigs(st, others)
	if err != nil {
		return nil
	}
	return configs
}

// --- ScreenCast session ---------------------------------------------------------

// mutterCast is a ScreenCast session recording a virtual monitor; an
// interface so tests can stand in for Mutter.
type mutterCast interface {
	// Monitor is the virtual monitor's DisplayConfig identity.
	Monitor() monitorSpec
	// Node is the PipeWire node the monitor is rendered into.
	Node() uint32
	// Closed is closed once the session has ended: stopped from GNOME's
	// screen-sharing indicator, or Mutter went away.
	Closed() <-chan struct{}
	// Changed receives a value when the monitor configuration changes.
	Changed() <-chan struct{}
	State() (mutterState, error)
	// Apply applies a configuration temporarily (ApplyMonitorsConfig).
	Apply(serial uint32, logical []mutterLogicalConfig, props map[string]dbus.Variant) error
	// ConsumerSized: the monitor has the one size the stream's PipeWire
	// consumer asked for (Mutter 49 and older; see awaitNewMutterMonitor).
	ConsumerSized() bool
	// Stop ends the session, which removes the virtual monitor.
	Stop()
	// Close disconnects from the session bus.
	Close()
}

// startMutterCast creates a virtual monitor offering w x h (preferred) and
// returns the session together with the monitor state from just before the
// monitor appeared. A variable for tests.
var startMutterCast = startMutterScreenCast

type mutterScreenCast struct {
	conn          *dbus.Conn
	spec          monitorSpec
	consumerSized bool
	signals       chan *dbus.Signal
	changed       chan struct{}

	mu        sync.Mutex
	session   dbus.ObjectPath
	stream    dbus.ObjectPath
	node      uint32
	nodeReady chan struct{}
	nodeOnce  sync.Once

	closed    chan struct{}
	closeOnce sync.Once

	// stopCursor ends the cursor companion stream (mutter_cursor_linux.go).
	stopCursor func()
}

func startMutterScreenCast(w, h, nativeW, nativeH int) (mutterCast, mutterState, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, mutterState{}, fmt.Errorf("session bus: %w", err)
	}
	c := &mutterScreenCast{
		conn:      conn,
		signals:   make(chan *dbus.Signal, 32),
		changed:   make(chan struct{}, 1),
		nodeReady: make(chan struct{}),
		closed:    make(chan struct{}),
	}
	conn.Signal(c.signals)
	go c.dispatch()
	ok := false
	defer func() {
		if !ok {
			c.Stop()
			c.Close()
		}
	}()

	if err := c.match(
		dbus.WithMatchObjectPath(displayConfigObjectPath),
		dbus.WithMatchInterface(displayConfigIface),
		dbus.WithMatchMember("MonitorsChanged"),
	); err != nil {
		return nil, mutterState{}, err
	}
	if err := c.match(
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, mutterBusName),
	); err != nil {
		return nil, mutterState{}, err
	}
	before, err := c.State()
	if err != nil {
		return nil, mutterState{}, err
	}

	var session dbus.ObjectPath
	if err := c.call(mutterBusName, mutterObjectPath, mutterIface+".CreateSession", map[string]dbus.Variant{}).Store(&session); err != nil {
		return nil, mutterState{}, fmt.Errorf("CreateSession: %w", err)
	}
	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	if err := c.match(
		dbus.WithMatchObjectPath(session),
		dbus.WithMatchInterface(mutterSessionIface),
		dbus.WithMatchMember("Closed"),
	); err != nil {
		return nil, mutterState{}, err
	}

	// cursor-mode 1 embeds the real cursor (whatever shape it has) in the
	// video, so physical-mouse and touch/pen movement both show the actual
	// host pointer, as ddagrab does on Windows. Mutter only paints it on a
	// virtual monitor with the cursor companion stream running (see
	// mutter_cursor_linux.go).
	//
	// Known limitation (accepted): Mutter draws the physical mouse pointer
	// and the Drawsplay Pen's tablet tool as two cursor sprites (a known
	// GNOME/Mutter bug class, see e.g. GNOME GitLab #3613), so both can
	// show in the frame at once. An Android-side cursor overlay was tried
	// and reverted: it can only follow touch/pen, so it vanished whenever
	// the physical mouse was used, which is worse.
	recordProps := map[string]dbus.Variant{
		"cursor-mode": dbus.MakeVariant(uint32(1)),
		"modes":       dbus.MakeVariant(virtualMonitorModes(w, h, nativeW, nativeH)),
	}
	var stream dbus.ObjectPath
	if err := c.call(mutterBusName, session, mutterSessionIface+".RecordVirtual", recordProps).Store(&stream); err != nil {
		return nil, mutterState{}, fmt.Errorf("RecordVirtual: %w", err)
	}
	c.mu.Lock()
	c.stream = stream
	c.mu.Unlock()
	if err := c.match(
		dbus.WithMatchObjectPath(stream),
		dbus.WithMatchInterface(mutterStreamIface),
		dbus.WithMatchMember("PipeWireStreamAdded"),
	); err != nil {
		return nil, mutterState{}, err
	}
	if err := c.call(mutterBusName, session, mutterSessionIface+".Start").Err; err != nil {
		return nil, mutterState{}, fmt.Errorf("session start: %w", err)
	}
	timeout := time.NewTimer(pipewireNodeTimeout)
	defer timeout.Stop()
	select {
	case <-c.nodeReady:
	case <-c.closed:
		return nil, mutterState{}, errors.New("the screen cast session ended before its stream started")
	case <-timeout.C:
		return nil, mutterState{}, fmt.Errorf("no PipeWireStreamAdded signal within %s", pipewireNodeTimeout)
	}
	if c.spec, c.consumerSized, err = awaitNewMutterMonitor(c, before, w, h); err != nil {
		return nil, mutterState{}, err
	}
	stopCursor := startMutterCursorCompanion(c.spec.Connector, c.closed)
	c.mu.Lock()
	c.stopCursor = stopCursor
	c.mu.Unlock()
	ok = true
	return c, before, nil
}

var errMutterMonitorNotListed = errors.New("Mutter did not list the virtual monitor")

// awaitNewMutterMonitor finds the virtual monitor c's stream records.
//
// Mutter 50 creates the monitor from RecordVirtual's "modes" before it
// announces the stream. Mutter 49 and older ignore "modes": they create the
// monitor once a PipeWire consumer agrees a frame size with the stream, at
// that size and with that one mode, and keep it until the session stops (a
// consumer asking for another size later resizes it). For them a throwaway
// consumer asks for w x h, made even for the encoders (primeMutterStream),
// so the monitor is there before the session's pipeline starts and can be
// arranged like Mutter 50's. consumerSized reports such a monitor: it has
// just the one mode, where Mutter 50's offers the presets too.
func awaitNewMutterMonitor(c mutterCast, before mutterState, w, h int) (spec monitorSpec, consumerSized bool, err error) {
	spec, err = findNewMutterMonitor(c, before, w, h, mutterPrimeAfter)
	if !errors.Is(err, errMutterMonitorNotListed) {
		return spec, false, err
	}
	w, h = w&^1, h&^1
	stop, perr := primeMutterStream(c.Node(), w, h)
	if perr != nil {
		return monitorSpec{}, false, fmt.Errorf("%w, and asking for it with a PipeWire consumer failed: %v", err, perr)
	}
	defer stop()
	if spec, err = findNewMutterMonitor(c, before, w, h, mutterPrimeAttempts); err != nil {
		return monitorSpec{}, false, err
	}
	st, err := c.State()
	if err != nil {
		return monitorSpec{}, false, err
	}
	m, ok := st.monitor(spec.Connector)
	return spec, ok && len(m.Modes) == 1, nil
}

// findNewMutterMonitor looks for a monitor that before does not list,
// preferring one whose preferred mode is w x h, up to attempts times.
func findNewMutterMonitor(c mutterCast, before mutterState, w, h, attempts int) (monitorSpec, error) {
	known := map[monitorSpec]bool{}
	for _, m := range before.Monitors {
		known[m.Spec] = true
	}
	for attempt := 1; ; attempt++ {
		st, err := c.State()
		if err != nil {
			return monitorSpec{}, err
		}
		var pick *monitorSpec
		for i, m := range st.Monitors {
			if known[m.Spec] {
				continue
			}
			if pick == nil {
				pick = &st.Monitors[i].Spec
			}
			if mode, ok := m.mode(w, h); ok && mode.Preferred {
				pick = &st.Monitors[i].Spec
				break
			}
		}
		if pick != nil {
			return *pick, nil
		}
		if attempt >= attempts {
			return monitorSpec{}, errMutterMonitorNotListed
		}
		time.Sleep(mutterLookupDelay)
	}
}

// resizeConsumerSizedMonitor changes a Mutter 49-or-older virtual monitor
// to w x h the way that Mutter sizes it, through the stream's consumer, and
// keeps it on its side of the other displays, which go back where they
// were: Mutter lays the desktop out afresh when a monitor changes size. The
// caller holds mutterExt.mu.
func resizeConsumerSizedMonitor(c mutterCast, w, h int) error {
	connector := c.Monitor().Connector
	pre, err := c.State()
	if err != nil {
		return err
	}
	side := mutterExt.placement
	if obs := observeMutterMonitor(pre, connector); obs.Placed {
		p := obs.Placement
		side, mutterExt.seen = &p, &p
	}
	others := pre
	others.Logical = mutterOtherLogical(pre, connector)

	stop, err := primeMutterStream(c.Node(), w, h)
	if err != nil {
		return err
	}
	resized := false
	for attempt := 1; !resized; attempt++ {
		st, err := c.State()
		if err != nil {
			stop()
			return err
		}
		if m, ok := st.monitor(connector); ok {
			_, resized = m.mode(w, h)
		}
		if !resized && attempt >= mutterPrimeAttempts {
			stop()
			return fmt.Errorf("%w %dx%d after asking for it", errMutterModeMissing, w, h)
		}
		if !resized {
			time.Sleep(mutterLookupDelay)
		}
	}
	stop()
	log.Printf("GNOME virtual monitor %s: resized to %dx%d", connector, w, h)
	_, err = applyMutterLayout(c, w, h, side, &others)
	return err
}

// primeMutterStream connects a throwaway PipeWire consumer to node asking
// for w x h; stop disconnects it. A variable for tests.
var primeMutterStream = gstPrimePipeWireStream

func gstPrimePipeWireStream(node uint32, w, h int) (func(), error) {
	cmd := exec.Command("gst-launch-1.0", "-q",
		"pipewiresrc", fmt.Sprintf("path=%d", node), "!",
		fmt.Sprintf("video/x-raw,width=%d,height=%d", w, h), "!",
		"fakesink", "sync=false")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return func() {
		_ = cmd.Process.Kill()
		<-done
	}, nil
}

func (c *mutterScreenCast) match(options ...dbus.MatchOption) error {
	if err := c.conn.AddMatchSignal(options...); err != nil {
		return fmt.Errorf("D-Bus signal match: %w", err)
	}
	return nil
}

func (c *mutterScreenCast) call(dest string, path dbus.ObjectPath, method string, args ...interface{}) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), mutterCallTimeout)
	defer cancel()
	return c.conn.Object(dest, path).CallWithContext(ctx, method, 0, args...)
}

// dispatch routes the connection's signals until it closes.
func (c *mutterScreenCast) dispatch() {
	for sig := range c.signals {
		c.mu.Lock()
		session, stream := c.session, c.stream
		c.mu.Unlock()
		switch sig.Name {
		case displayConfigIface + ".MonitorsChanged":
			select {
			case c.changed <- struct{}{}:
			default:
			}
		case "org.freedesktop.DBus.NameOwnerChanged":
			// Mutter left the bus (GNOME Shell exited or restarted).
			if len(sig.Body) == 3 && sig.Body[0] == mutterBusName && sig.Body[1] != "" {
				c.markClosed()
			}
		case mutterSessionIface + ".Closed":
			if session != "" && sig.Path == session {
				c.markClosed()
			}
		case mutterStreamIface + ".PipeWireStreamAdded":
			if node, ok := firstUint32(sig.Body); ok && stream != "" && sig.Path == stream {
				c.mu.Lock()
				c.node = node
				c.mu.Unlock()
				c.nodeOnce.Do(func() { close(c.nodeReady) })
			}
		}
	}
	c.markClosed()
}

func firstUint32(body []interface{}) (uint32, bool) {
	if len(body) != 1 {
		return 0, false
	}
	v, ok := body[0].(uint32)
	return v, ok
}

func (c *mutterScreenCast) markClosed() {
	c.closeOnce.Do(func() { close(c.closed) })
}

func (c *mutterScreenCast) ConsumerSized() bool { return c.consumerSized }

func (c *mutterScreenCast) Monitor() monitorSpec { return c.spec }

func (c *mutterScreenCast) Node() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.node
}

func (c *mutterScreenCast) Closed() <-chan struct{}  { return c.closed }
func (c *mutterScreenCast) Changed() <-chan struct{} { return c.changed }

func (c *mutterScreenCast) State() (mutterState, error) {
	call := c.call(displayConfigBusName, displayConfigObjectPath, displayConfigIface+".GetCurrentState")
	if call.Err != nil {
		return mutterState{}, fmt.Errorf("GetCurrentState: %w", call.Err)
	}
	return parseMutterState(call.Body)
}

func (c *mutterScreenCast) Apply(serial uint32, logical []mutterLogicalConfig, props map[string]dbus.Variant) error {
	call := c.call(displayConfigBusName, displayConfigObjectPath, displayConfigIface+".ApplyMonitorsConfig",
		serial, uint32(mutterApplyTemporary), logical, props)
	if call.Err != nil {
		return fmt.Errorf("ApplyMonitorsConfig: %w", call.Err)
	}
	return nil
}

func (c *mutterScreenCast) Stop() {
	c.mu.Lock()
	session, stopCursor := c.session, c.stopCursor
	c.stopCursor = nil
	c.mu.Unlock()
	if stopCursor != nil {
		stopCursor()
	}
	if session == "" {
		return
	}
	select {
	case <-c.closed:
		return
	default:
	}
	if err := c.call(mutterBusName, session, mutterSessionIface+".Stop").Err; err != nil {
		log.Printf("GNOME virtual monitor: stopping the screen cast session: %v", err)
	}
}

func (c *mutterScreenCast) Close() {
	_ = c.conn.Close()
}

// mutterScreenCastAvailable reports whether GNOME's ScreenCast API is on
// the session bus; other Wayland desktops do not offer it.
func mutterScreenCastAvailable() (bool, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), mutterCallTimeout)
	defer cancel()
	var has bool
	err = conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, mutterBusName).Store(&has)
	return has, err
}

func mutterCastClosed(c mutterCast) bool {
	select {
	case <-c.Closed():
		return true
	default:
		return false
	}
}

// --- virtual monitor across sessions -------------------------------------------

// mutterExt holds the virtual monitor between sessions. mu also serializes
// every layout change the host makes.
var mutterExt struct {
	mu sync.Mutex
	// gen identifies the latest session to claim the monitor; claimed is
	// whether that session still runs.
	gen     uint64
	claimed bool
	timer   *time.Timer
	held    mutterCast
	// others identifies the other monitors when the host put them back as
	// they were before the virtual monitor appeared; they are put back
	// again when it goes, unless they changed in the meantime.
	others string
	// seen is where the user last had the held monitor; placement is where
	// it was when the last one went (process lifetime).
	seen, placement *x11Placement
	exiting         bool
}

func stopMutterReleaseTimerLocked() {
	if mutterExt.timer != nil {
		mutterExt.timer.Stop()
		mutterExt.timer = nil
	}
}

// releaseMutterLocked ends session gen's claim and removes the monitor after
// mutterReleaseGrace unless another session claims it first.
func releaseMutterLocked(gen uint64) {
	if gen != mutterExt.gen {
		return
	}
	mutterExt.claimed = false
	stopMutterReleaseTimerLocked()
	if mutterExt.exiting || mutterExt.held == nil {
		return
	}
	mutterExt.timer = time.AfterFunc(mutterReleaseGrace, func() {
		mutterExt.mu.Lock()
		defer mutterExt.mu.Unlock()
		if gen != mutterExt.gen || mutterExt.claimed || mutterExt.exiting {
			return
		}
		mutterExt.timer = nil
		stopHeldMutterLocked()
	})
}

// releaseMutterOnExit removes the virtual monitor and puts the other
// displays back; ending the session by just disconnecting would leave them
// as Mutter lays them out.
func releaseMutterOnExit() {
	mutterExt.mu.Lock()
	defer mutterExt.mu.Unlock()
	mutterExt.exiting = true
	stopMutterReleaseTimerLocked()
	stopHeldMutterLocked()
}

// stopHeldMutterLocked removes the held virtual monitor, remembering where
// it was, and keeps the other displays where they are (see
// planMutterRelease).
func stopHeldMutterLocked() {
	c := mutterExt.held
	if c == nil {
		return
	}
	connector := c.Monitor().Connector
	var restore []mutterLogicalConfig
	var from mutterState // the state restore is planned from
	alive := !mutterCastClosed(c)
	if alive {
		if st, err := c.State(); err == nil {
			if obs := observeMutterMonitor(st, connector); obs.Placed {
				p := obs.Placement
				mutterExt.seen = &p
			}
			if mutterExt.others != "" && mutterExt.others == mutterOthersSignature(st, connector) {
				restore, from = planMutterRelease(st, connector), st
			}
		}
	}
	if mutterExt.seen != nil {
		mutterExt.placement = mutterExt.seen
	}
	mutterExt.held, mutterExt.seen, mutterExt.others = nil, nil, ""
	c.Stop()
	if alive {
		// Without a stored configuration for the remaining monitors, Mutter
		// lays them out in a row, in its default layout mode, ending leases.
		if st, gone := awaitMutterMonitorGone(c, connector); gone && restore != nil && !mutterShows(st, from, restore) {
			restoreMutterLayout(c, from, restore)
		}
	}
	c.Close()
	log.Printf("GNOME virtual monitor %s removed", connector)
}

// awaitMutterMonitorGone waits for Mutter to drop the monitor, so the state
// that follows is the desktop without it.
func awaitMutterMonitorGone(c mutterCast, connector string) (mutterState, bool) {
	for attempt := 1; ; attempt++ {
		st, err := c.State()
		if err != nil {
			return st, false
		}
		if _, ok := st.monitor(connector); !ok {
			return st, true
		}
		if attempt >= mutterLookupAttempts {
			return st, false
		}
		time.Sleep(mutterLookupDelay)
	}
}

// restoreMutterLayout applies restore, planned from the state from, with
// from's layout mode and leases.
func restoreMutterLayout(c mutterCast, from mutterState, restore []mutterLogicalConfig) {
	var lastErr error
	for attempt := 0; attempt < mutterApplyAttempts; attempt++ {
		st, err := c.State()
		if err != nil {
			lastErr = err
			break
		}
		if mutterShows(st, from, restore) {
			return
		}
		if lastErr = c.Apply(st.Serial, restore, mutterApplyProps(st, from)); lastErr == nil {
			log.Printf("GNOME virtual monitor: kept the other displays where they were")
			return
		}
	}
	log.Printf("GNOME virtual monitor: could not keep the other displays where they were: %v", lastErr)
}

// applyMutterLayout arranges the displays for a w x h virtual monitor (see
// planMutterLayout), retrying when the monitors change underneath it. The
// caller holds mutterExt.mu.
func applyMutterLayout(c mutterCast, w, h int, remembered *x11Placement, before *mutterState) (mutterLayout, error) {
	connector := c.Monitor().Connector
	var lastErr error
	for attempt := 0; attempt < mutterApplyAttempts; attempt++ {
		st, err := c.State()
		if err != nil {
			return mutterLayout{}, err
		}
		layout, err := planMutterLayout(st, connector, w, h, remembered, before)
		if err != nil {
			return mutterLayout{}, err
		}
		if layout.Keep {
			return layout, nil
		}
		if lastErr = c.Apply(st.Serial, layout.Configs, layout.Props); lastErr == nil {
			log.Printf("GNOME virtual monitor %s: set to %dx%d at +%d+%d", connector, w, h, layout.Rect.X, layout.Rect.Y)
			return layout, nil
		}
	}
	return mutterLayout{}, lastErr
}

// ensureMutterMonitorLocked makes the held virtual monitor show w x h next
// to the other displays, creating one when none is held or the held one
// does not offer w x h. The caller holds mutterExt.mu.
func ensureMutterMonitorLocked(w, h, nativeW, nativeH int) error {
	if c := mutterExt.held; c != nil {
		var err error
		if !mutterCastClosed(c) {
			cw, ch := w, h
			if c.ConsumerSized() {
				cw, ch = w&^1, h&^1
			}
			_, err = applyMutterLayout(c, cw, ch, mutterExt.placement, nil)
			if errors.Is(err, errMutterModeMissing) && c.ConsumerSized() {
				err = resizeConsumerSizedMonitor(c, cw, ch)
			}
		}
		if mutterCastClosed(c) {
			stopHeldMutterLocked()
		} else if errors.Is(err, errMutterModeMissing) {
			log.Printf("GNOME virtual monitor %s: %v; replacing it", c.Monitor().Connector, err)
			stopHeldMutterLocked()
		} else {
			if err != nil {
				log.Printf("GNOME virtual monitor %s: could not arrange the displays (%v); keeping the current layout", c.Monitor().Connector, err)
			}
			return nil
		}
	}
	c, before, err := startMutterCast(w, h, nativeW, nativeH)
	if err != nil {
		return err
	}
	mutterExt.held, mutterExt.seen, mutterExt.others = c, nil, ""
	log.Printf("GNOME virtual monitor %s created (PipeWire node %d)", c.Monitor().Connector, c.Node())
	if c.ConsumerSized() {
		w, h = w&^1, h&^1
	}
	layout, err := applyMutterLayout(c, w, h, mutterExt.placement, &before)
	if err != nil {
		log.Printf("GNOME virtual monitor %s: could not arrange the displays (%v); keeping GNOME's layout", c.Monitor().Connector, err)
		return nil
	}
	mutterExt.others = layout.Others
	return nil
}

// mapMutterTablet points the Drawsplay Pen's "Map to Monitor" setting at the
// virtual monitor. Mutter keys the setting by the monitor's serial, which is
// new for every virtual monitor.
func mapMutterTablet(spec monitorSpec) {
	mapGsettingsTouchscreen(mutterTabletCompositor, spec)
	if err := setTabletOutput(mutterTabletCompositor, spec); err != nil {
		log.Printf("GNOME virtual monitor: could not map Drawsplay Pen input to %s (%v); touch/pen input may land on another monitor", spec.Connector, err)
		return
	}
	log.Printf("Mapped Drawsplay Pen touch/pen input to %s (Mutter)", spec.Connector)
}

// --- one session's display ---------------------------------------------------------

// mutterObserved is the virtual monitor's state as the watcher saw it.
type mutterObserved struct {
	Present, Enabled bool
	Rect             displayRect
	ModeW, ModeH     int
	// AutoMode: the monitor shows its preferred mode, which Mutter picks by
	// itself when it lays the desktop out after a hotplug.
	AutoMode bool
	// Mirrored: the monitor shows the same picture as another display.
	Mirrored bool
	// Placement is the monitor's place next to the other displays, when
	// Placed.
	Placement x11Placement
	Placed    bool
	// Outputs lists the monitors and their modes; Mutter lays the desktop
	// out again when they change (a hotplug).
	Outputs string
}

func observeMutterMonitor(st mutterState, connector string) mutterObserved {
	var outputs strings.Builder
	for _, m := range st.Monitors {
		outputs.WriteString(m.Spec.Connector)
		for _, mode := range m.Modes {
			outputs.WriteString(" " + mode.ID)
		}
		outputs.WriteString(";")
	}
	obs := mutterObserved{Outputs: outputs.String()}
	m, ok := st.monitor(connector)
	if !ok {
		return obs
	}
	obs.Present = true
	if mode, ok := m.currentMode(); ok {
		obs.ModeW, obs.ModeH, obs.AutoMode = mode.W, mode.H, mode.Preferred
	}
	i := st.logicalIndex(connector)
	if i < 0 {
		return obs
	}
	obs.Enabled = true
	obs.Rect = st.rect(st.Logical[i])
	obs.Mirrored = len(st.Logical[i].Monitors) > 1
	var rects []displayRect
	for j, lm := range st.Logical {
		if j != i {
			rects = append(rects, st.rect(lm))
		}
	}
	if len(rects) > 0 && !obs.Mirrored {
		obs.Placement, obs.Placed = placementOf(obs.Rect, boundsOf(rects))
	}
	return obs
}

func mutterTarget(st mutterState, c mutterCast) (captureTarget, error) {
	connector := c.Monitor().Connector
	obs := observeMutterMonitor(st, connector)
	if !obs.Present {
		return captureTarget{}, fmt.Errorf("GNOME virtual monitor %s is gone", connector)
	}
	if !obs.Enabled {
		return captureTarget{}, fmt.Errorf("GNOME virtual monitor %s is disabled", connector)
	}
	return captureTarget{
		Kind: capturePipeWire,
		Node: c.Node(),
		// Mutter streams the monitor at its mode's size, even rotated.
		// Encoders need even dimensions.
		Width:        obs.ModeW &^ 1,
		Height:       obs.ModeH &^ 1,
		Rect:         obs.Rect,
		TabletMapped: true,
		Mirrored:     obs.Mirrored,
		// A consumer-sized monitor follows the size the pipeline asks for,
		// and Settings -> Displays offers it no other.
		SizeFromConsumer: c.ConsumerSized(),
		HostResolution:   c.ConsumerSized(),
	}, nil
}

// mutterDisplay is one session's use of the virtual monitor.
type mutterDisplay struct {
	gen              uint64
	nativeW, nativeH int

	mu         sync.Mutex
	cast       mutterCast
	target     captureTarget
	reqW, reqH int // configured resolution
	// recovering: Recover is replacing the monitor, so its removal does not
	// restart the session.
	recovering bool

	swapped   chan struct{} // Recover replaced cast
	stopWatch chan struct{}
	watchDone chan struct{}
	closeOnce sync.Once
}

// newMutterDisplay creates (or keeps, after a quick reconnect) the virtual
// monitor for a new session at the configured resolution.
func newMutterDisplay(opts videoOptions) (*mutterDisplay, error) {
	w, h := opts.Width, opts.Height
	if w <= 0 || h <= 0 {
		w, h = x11DefaultWidth, x11DefaultHeight
	}
	ident := activeClientIdentity()

	mutterExt.mu.Lock()
	defer mutterExt.mu.Unlock()
	if mutterExt.exiting {
		return nil, errors.New("host is shutting down")
	}
	mutterExt.gen++
	gen := mutterExt.gen
	mutterExt.claimed = true
	stopMutterReleaseTimerLocked()

	if err := ensureMutterMonitorLocked(w, h, ident.NativeWidth, ident.NativeHeight); err != nil {
		releaseMutterLocked(gen)
		return nil, err
	}
	c := mutterExt.held
	st, err := c.State()
	var t captureTarget
	if err == nil {
		t, err = mutterTarget(st, c)
	}
	if err != nil {
		stopHeldMutterLocked()
		releaseMutterLocked(gen)
		return nil, err
	}
	mapMutterTablet(c.Monitor())
	d := &mutterDisplay{
		gen:       gen,
		nativeW:   ident.NativeWidth,
		nativeH:   ident.NativeHeight,
		cast:      c,
		target:    t,
		reqW:      w,
		reqH:      h,
		swapped:   make(chan struct{}, 1),
		stopWatch: make(chan struct{}),
		watchDone: make(chan struct{}),
	}
	go d.watch(observeMutterMonitor(st, c.Monitor().Connector))
	return d, nil
}

func (d *mutterDisplay) Target() captureTarget {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.target
}

func (d *mutterDisplay) Description() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.target
	return fmt.Sprintf("GNOME virtual monitor %s (PipeWire node %d, %dx%d at +%d+%d)",
		d.cast.Monitor().Connector, t.Node, t.Width, t.Height, t.Rect.X, t.Rect.Y)
}

// Refresh re-reads the monitor's place in the desktop, which the user can
// change in Settings -> Displays at any time.
func (d *mutterDisplay) Refresh() (captureTarget, error) {
	d.mu.Lock()
	c := d.cast
	d.mu.Unlock()
	if mutterCastClosed(c) {
		return captureTarget{}, fmt.Errorf("GNOME virtual monitor %s was removed", c.Monitor().Connector)
	}
	st, err := c.State()
	if err != nil {
		// The stream does not depend on DisplayConfig.
		throttledLog("mutter-refresh", "GNOME virtual monitor: could not read the display configuration (%v); keeping the last one", err)
		return d.Target(), nil
	}
	t, err := mutterTarget(st, c)
	if err != nil {
		return captureTarget{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cast == c {
		d.target = t
	}
	return d.target, nil
}

// Recover replaces the virtual monitor after repeated capture failures: a
// new monitor comes with a new PipeWire stream.
func (d *mutterDisplay) Recover() error {
	mutterExt.mu.Lock()
	defer mutterExt.mu.Unlock()
	if mutterExt.exiting {
		return errors.New("host is shutting down")
	}
	if d.gen != mutterExt.gen {
		return errors.New("a newer session uses the GNOME virtual monitor")
	}
	d.mu.Lock()
	w, h, old := d.reqW, d.reqH, d.cast
	d.recovering = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.recovering = false
		d.mu.Unlock()
	}()
	if mutterExt.held == old {
		stopHeldMutterLocked()
	}
	if err := ensureMutterMonitorLocked(w, h, d.nativeW, d.nativeH); err != nil {
		return err
	}
	c := mutterExt.held
	st, err := c.State()
	if err != nil {
		return err
	}
	t, err := mutterTarget(st, c)
	if err != nil {
		return err
	}
	mapMutterTablet(c.Monitor())
	d.mu.Lock()
	d.cast, d.target = c, t
	d.mu.Unlock()
	select {
	case d.swapped <- struct{}{}:
	default:
	}
	return nil
}

func (d *mutterDisplay) Close() {
	d.closeOnce.Do(func() {
		close(d.stopWatch)
		<-d.watchDone
		mutterExt.mu.Lock()
		releaseMutterLocked(d.gen)
		mutterExt.mu.Unlock()
	})
}

// watch follows the monitor while the session runs, like the X11 extended
// display's watcher: a resolution picked in Settings -> Displays is saved as
// the configured one and restarts the session, as do mirroring, switching
// the monitor off and stopping the screen share. A move only updates the
// monitor's place; capture follows the monitor anyway. base is the state the
// session started from.
func (d *mutterDisplay) watch(base mutterObserved) {
	defer close(d.watchDone)
	d.mu.Lock()
	c := d.cast
	d.mu.Unlock()
	closed, changed := c.Closed(), c.Changed()
	var settle *time.Timer
	var settled <-chan time.Time
	defer func() {
		if settle != nil {
			settle.Stop()
		}
	}()
	for {
		select {
		case <-d.stopWatch:
			return
		case <-d.swapped:
			d.mu.Lock()
			c = d.cast
			d.mu.Unlock()
			closed, changed = c.Closed(), c.Changed()
			if st, err := c.State(); err == nil {
				base = observeMutterMonitor(st, c.Monitor().Connector)
			}
			continue
		case <-closed:
			if d.replacing(c) {
				closed = nil
				continue
			}
			log.Printf("GNOME virtual monitor %s was removed (screen sharing stopped); restarting video session", c.Monitor().Connector)
			restartActiveVideoSession()
			return
		case <-changed:
			if settle != nil {
				settle.Stop()
			}
			settle = time.NewTimer(mutterSettle)
			settled = settle.C
			continue
		case <-settled:
			settled = nil
		}
		if d.replacing(c) {
			continue
		}
		connector := c.Monitor().Connector
		st, err := c.State()
		if err != nil {
			throttledLog("mutter-watch", "GNOME virtual monitor %s: could not read the display configuration: %v", connector, err)
			continue
		}
		obs := observeMutterMonitor(st, connector)
		// Mutter switches the monitor to its preferred mode by itself when
		// a monitor is plugged in or out; otherwise the preferred mode, too,
		// was picked by the user.
		hotplug := base.Outputs != obs.Outputs
		// Settings -> Displays offers a consumer-sized monitor no other
		// mode: its size is the host's.
		d.saveUserResolution(obs, (obs.AutoMode && hotplug) || c.ConsumerSized())
		base = obs
		if obs.Placed {
			p := obs.Placement
			mutterExt.mu.Lock()
			if mutterExt.held == c {
				mutterExt.seen = &p
			}
			mutterExt.mu.Unlock()
		}
		d.mu.Lock()
		if d.recovering || d.cast != c {
			// Recover started meanwhile: obs may show the old monitor
			// going away.
			d.mu.Unlock()
			continue
		}
		t := d.target
		restart := !obs.Enabled || obs.ModeW&^1 != t.Width || obs.ModeH&^1 != t.Height || obs.Mirrored != t.Mirrored
		moved := !restart && obs.Rect != t.Rect
		if moved {
			d.target.Rect = obs.Rect
		}
		d.mu.Unlock()
		switch {
		case moved:
			log.Printf("GNOME virtual monitor %s moved to +%d+%d", connector, obs.Rect.X, obs.Rect.Y)
		case !restart:
		case !obs.Enabled:
			log.Printf("GNOME virtual monitor %s was switched off; restarting video session", connector)
		case obs.Mirrored:
			log.Printf("GNOME virtual monitor %s now mirrors another display at %dx%d; restarting video session", connector, obs.ModeW, obs.ModeH)
		default:
			log.Printf("GNOME virtual monitor %s is now %dx%d; restarting video session", connector, obs.ModeW, obs.ModeH)
		}
		if restart {
			restartActiveVideoSession()
			return
		}
	}
}

// replacing reports whether Recover is replacing c, the monitor the watcher
// follows: the monitor's removal and the changes around it are not the
// user's.
func (d *mutterDisplay) replacing(c mutterCast) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.recovering || d.cast != c
}

// saveUserResolution saves the monitor's mode as the configured resolution
// when the user picked a different one, so the next session (and the
// restart that follows the change) keeps it. auto: Mutter picked the mode by
// itself, so it is not saved.
func (d *mutterDisplay) saveUserResolution(obs mutterObserved, auto bool) {
	w, h := obs.ModeW, obs.ModeH
	d.mu.Lock()
	reqW, reqH := d.reqW, d.reqH
	if !obs.Enabled || w <= 0 || h <= 0 || (w == reqW && h == reqH) || auto {
		d.mu.Unlock()
		return
	}
	d.reqW, d.reqH = w, h
	d.mu.Unlock()
	if globalSettingsStore == nil {
		return
	}
	if err := globalSettingsStore.updateResolution(w, h); err != nil {
		log.Printf("Could not save the GNOME virtual monitor resolution %dx%d: %v", w, h, err)
		return
	}
	log.Printf("GNOME virtual monitor resolution changed in Settings (%dx%d -> %dx%d); saved as the configured resolution", reqW, reqH, w, h)
}
