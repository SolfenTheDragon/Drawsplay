//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// --- fake Mutter --------------------------------------------------------------

// fakeMutter stands in for Mutter's DisplayConfig and ScreenCast APIs. Like
// Mutter (meta_monitor_manager_ensure_configured) it lays the desktop out
// again whenever the set of monitors changes: as stored for that set, else
// as the current or previous configuration if made for that set, else in a
// row at preferred modes and scales and the default layout mode, the
// primary first. It checks applied configurations by Mutter's rules
// (meta-monitor-config-utils.c) and adds a virtual monitor per screen cast.
type fakeMutter struct {
	mu                  sync.Mutex
	serial              uint32
	monitors            []*fakeMutterMonitor // physical first, as Mutter lists them
	logical             []mutterLogicalMonitor
	layoutMode          uint32
	defaultLayoutMode   uint32
	canChangeLayoutMode bool
	// stored holds what the user set in Settings -> Displays, per set of
	// monitors, like monitors.xml.
	stored map[string]fakeMutterConfig
	// current and history (newest first) are the configurations shown, as
	// meta_monitor_config_manager_set_current keeps them.
	current       *fakeMutterConfig
	history       []fakeMutterConfig
	casts         []*fakeMutterCast
	applies       int
	virtualSerial int
	// crashed: an applied configuration left a virtual monitor out, which
	// makes GNOME Shell 50 crash.
	crashed   bool
	failState error
	startErr  error
	// legacy behaves like Mutter 49 and older: RecordVirtual ignores the
	// modes, and the stream's virtual monitor comes when a PipeWire
	// consumer asks it for a size (see prime).
	legacy   bool
	primes   int
	primeErr error
}

type fakeMutterMonitor struct {
	spec     monitorSpec
	modes    []mutterMode // Current is unused; see current
	current  string       // current mode ID; "" when off
	props    map[string]dbus.Variant
	forLease bool
	virtual  bool
}

type fakeMutterConfig struct {
	key        string // the set of monitors it is for
	logical    []mutterLogicalMonitor
	layoutMode uint32
	modes      map[string]string
	props      map[string]map[string]dbus.Variant
	leased     map[string]bool
}

func fakeModeID(w, h int) string { return fmt.Sprintf("%dx%d@60.000", w, h) }

func fakeMutterModeOf(w, h int, preferred bool) mutterMode {
	return mutterMode{ID: fakeModeID(w, h), W: w, H: h, PreferredScale: 1, Scales: []float64{1, 2}, Preferred: preferred}
}

// fakePhysicalMonitor offers sizes, the first one preferred.
func fakePhysicalMonitor(connector, vendor, product, serial string, sizes ...[2]int) *fakeMutterMonitor {
	m := &fakeMutterMonitor{
		spec:  monitorSpec{Connector: connector, Vendor: vendor, Product: product, Serial: serial},
		props: map[string]dbus.Variant{},
	}
	for i, s := range sizes {
		m.modes = append(m.modes, fakeMutterModeOf(s[0], s[1], i == 0))
	}
	return m
}

func fakeLaptopPanel() *fakeMutterMonitor {
	return fakePhysicalMonitor("eDP-1", "BOE", "0x095f", "0x00000000", [2]int{1920, 1080}, [2]int{1280, 720})
}

func fakeExternalMonitor() *fakeMutterMonitor {
	return fakePhysicalMonitor("HDMI-1", "DEL", "DELL U2720Q", "8KTMX13", [2]int{2560, 1440}, [2]int{1920, 1080})
}

// fakeHiDPIPanel is a 2880x1800 laptop panel Mutter scales by 2.
func fakeHiDPIPanel() *fakeMutterMonitor {
	m := fakePhysicalMonitor("eDP-1", "SDC", "0x4171", "0x00000000", [2]int{2880, 1800})
	m.modes[0].PreferredScale = 2
	return m
}

func newFakeMutter(monitors ...*fakeMutterMonitor) *fakeMutter {
	f := &fakeMutter{
		serial:            1,
		monitors:          monitors,
		layoutMode:        mutterLayoutPhysical,
		defaultLayoutMode: mutterLayoutPhysical,
		stored:            map[string]fakeMutterConfig{},
	}
	f.relayoutLocked()
	return f
}

func newFakeLaptopMutter() *fakeMutter { return newFakeMutter(fakeLaptopPanel()) }

// fakeDeskArrangement is newFakeDeskMutter's desktop: an external monitor
// above the laptop panel, which is the primary display.
const fakeDeskArrangement = "HDMI-1@0,0 2560x1440 eDP-1@320,1440 1920x1080*"

func newFakeDeskMutter(t *testing.T) *fakeMutter {
	t.Helper()
	f := newFakeMutter(fakeLaptopPanel(), fakeExternalMonitor())
	f.configure(t, nil, fakeLM(0, 0, false, "HDMI-1"), fakeLM(320, 1440, true, "eDP-1"))
	return f
}

// inLogicalLayoutMode starts GNOME as with fractional scaling turned on:
// the logical layout mode is the default and can be changed.
func (f *fakeMutter) inLogicalLayoutMode() *fakeMutter {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaultLayoutMode, f.canChangeLayoutMode = mutterLayoutLogical, true
	f.current, f.history = nil, nil
	f.relayoutLocked()
	return f
}

// fakeLM is a logical monitor at scale 1 showing connectors.
func fakeLM(x, y int, primary bool, connectors ...string) mutterLogicalMonitor {
	lm := mutterLogicalMonitor{X: x, Y: y, Scale: 1, Primary: primary}
	for _, c := range connectors {
		lm.Monitors = append(lm.Monitors, monitorSpec{Connector: c})
	}
	return lm
}

func (f *fakeMutter) monitorLocked(connector string) *fakeMutterMonitor {
	for _, m := range f.monitors {
		if m.spec.Connector == connector {
			return m
		}
	}
	return nil
}

func (m *fakeMutterMonitor) modeByID(id string) (mutterMode, bool) {
	for _, mode := range m.modes {
		if mode.ID == id {
			return mode, true
		}
	}
	return mutterMode{}, false
}

func (m *fakeMutterMonitor) preferredMode() mutterMode {
	for _, mode := range m.modes {
		if mode.Preferred {
			return mode
		}
	}
	return m.modes[0]
}

func (f *fakeMutter) setKeyLocked() string {
	var specs []string
	for _, m := range f.monitors {
		specs = append(specs, fmt.Sprint(m.spec))
	}
	sort.Strings(specs)
	return strings.Join(specs, ";")
}

func copyFakeLogical(lms []mutterLogicalMonitor) []mutterLogicalMonitor {
	out := make([]mutterLogicalMonitor, len(lms))
	for i, lm := range lms {
		lm.Monitors = append([]monitorSpec(nil), lm.Monitors...)
		out[i] = lm
	}
	return out
}

func copyFakeProps(props map[string]dbus.Variant) map[string]dbus.Variant {
	out := map[string]dbus.Variant{}
	for k, v := range props {
		out[k] = v
	}
	return out
}

// snapshotLocked records the configuration shown.
func (f *fakeMutter) snapshotLocked() fakeMutterConfig {
	cfg := fakeMutterConfig{
		key:        f.setKeyLocked(),
		logical:    copyFakeLogical(f.logical),
		layoutMode: f.layoutMode,
		modes:      map[string]string{},
		props:      map[string]map[string]dbus.Variant{},
		leased:     map[string]bool{},
	}
	for _, m := range f.monitors {
		cfg.modes[m.spec.Connector] = m.current
		cfg.props[m.spec.Connector] = copyFakeProps(m.props)
		cfg.leased[m.spec.Connector] = m.forLease
	}
	return cfg
}

// setCurrentLocked makes the configuration shown the current one, the
// previous one going into the history (3 deep).
func (f *fakeMutter) setCurrentLocked() {
	if f.current != nil {
		f.history = append([]fakeMutterConfig{*f.current}, f.history...)
		if len(f.history) > 3 {
			f.history = f.history[:3]
		}
	}
	cfg := f.snapshotLocked()
	f.current = &cfg
}

func (f *fakeMutter) showConfigLocked(cfg fakeMutterConfig) {
	f.layoutMode = cfg.layoutMode
	f.logical = copyFakeLogical(cfg.logical)
	for _, m := range f.monitors {
		m.current = cfg.modes[m.spec.Connector]
		m.props = copyFakeProps(cfg.props[m.spec.Connector])
		m.forLease = cfg.leased[m.spec.Connector]
	}
}

// relayoutLocked arranges the desktop for a new set of monitors as Mutter's
// meta_monitor_manager_ensure_configured does: as stored for the set, else
// as the current or previous configuration if made for the set, else in a
// row (create_monitors_config) at preferred modes and scales, without
// rotation, in the default layout mode, the primary first, then the others
// as listed (virtual ones last). The row includes leased monitors, ending
// their lease, and keeps each monitor's settings (create_monitor_config).
func (f *fakeMutter) relayoutLocked() {
	key := f.setKeyLocked()
	var candidates []fakeMutterConfig
	if cfg, ok := f.stored[key]; ok {
		candidates = append(candidates, cfg)
	}
	if f.current != nil {
		candidates = append(candidates, *f.current)
	}
	if len(f.history) > 0 {
		candidates = append(candidates, f.history[0])
	}
	for _, cfg := range candidates {
		if cfg.key == key && f.offersModesLocked(cfg) {
			f.showConfigLocked(cfg)
			f.setCurrentLocked()
			return
		}
	}

	var primary *fakeMutterMonitor
	for _, lm := range f.logical {
		if m := f.monitorLocked(lm.Monitors[0].Connector); lm.Primary && m != nil {
			primary = m
		}
	}
	f.layoutMode = f.defaultLayoutMode
	f.logical = nil
	if len(f.monitors) == 0 {
		f.setCurrentLocked()
		return
	}
	if primary == nil {
		primary = f.monitors[0]
	}
	order := []*fakeMutterMonitor{primary}
	for _, virtual := range []bool{false, true} {
		for _, m := range f.monitors {
			if m.virtual == virtual && m != primary {
				order = append(order, m)
			}
		}
	}
	x := 0
	for _, m := range order {
		mode := m.preferredMode()
		m.current, m.forLease = mode.ID, false
		f.logical = append(f.logical, mutterLogicalMonitor{
			X: x, Scale: mode.PreferredScale, Primary: m == primary, Monitors: []monitorSpec{m.spec},
		})
		w, _ := mutterLogicalSize(mode.W, mode.H, mode.PreferredScale, 0, f.layoutMode)
		x += w
	}
	f.setCurrentLocked()
}

// offersModesLocked: Mutter applies a configuration only while its monitors
// offer its modes.
func (f *fakeMutter) offersModesLocked(cfg fakeMutterConfig) bool {
	for _, m := range f.monitors {
		if id := cfg.modes[m.spec.Connector]; id != "" {
			if _, ok := m.modeByID(id); !ok {
				return false
			}
		}
	}
	return true
}

func (f *fakeMutter) stateLocked() mutterState {
	st := mutterState{
		Serial:              f.serial,
		LayoutMode:          f.layoutMode,
		CanChangeLayoutMode: f.canChangeLayoutMode,
		Logical:             copyFakeLogical(f.logical),
	}
	for _, m := range f.monitors {
		mon := mutterMonitor{Spec: m.spec, Props: copyFakeProps(m.props), ForLease: m.forLease}
		for _, mode := range m.modes {
			mode.Current = mode.ID == m.current
			mode.Scales = append([]float64(nil), mode.Scales...)
			mon.Modes = append(mon.Modes, mode)
		}
		st.Monitors = append(st.Monitors, mon)
	}
	return st
}

func (f *fakeMutter) state() (mutterState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.crashed {
		return mutterState{}, errors.New("GNOME Shell crashed")
	}
	if f.failState != nil {
		return mutterState{}, f.failState
	}
	return f.stateLocked(), nil
}

// verifyLocked checks a configuration as Mutter does before applying it
// (meta_verify_logical_monitor_config_list): known monitors and modes, each
// monitor once, mirrored monitors at one size, supported scales (whole ones
// in the physical layout mode), one primary display, the desktop's corner
// at 0,0, no overlaps, and every display connected to the others through
// shared edges.
func (f *fakeMutter) verifyLocked(configs []mutterLogicalConfig, layoutMode uint32) error {
	if len(configs) == 0 {
		return errors.New("Monitors config incomplete")
	}
	used := map[string]bool{}
	rects := make([]displayRect, len(configs))
	primaries := 0
	for i, c := range configs {
		if len(c.Monitors) == 0 {
			return errors.New("Logical monitor is empty")
		}
		var w, h int
		for j, mc := range c.Monitors {
			m := f.monitorLocked(mc.Connector)
			if m == nil {
				return fmt.Errorf("Invalid connector %s", mc.Connector)
			}
			if used[mc.Connector] {
				return fmt.Errorf("%s configured twice", mc.Connector)
			}
			used[mc.Connector] = true
			mode, ok := m.modeByID(mc.ModeID)
			if !ok {
				return fmt.Errorf("Invalid mode %q for %s", mc.ModeID, mc.Connector)
			}
			if _, ok := mode.supports(c.Scale); !ok || (layoutMode == mutterLayoutPhysical && c.Scale != float64(int(c.Scale))) {
				return fmt.Errorf("Scale %v not valid for %s", c.Scale, mc.Connector)
			}
			if j > 0 && (mode.W != w || mode.H != h) {
				return errors.New("Monitor modes in logical monitor conflict")
			}
			w, h = mode.W, mode.H
		}
		lw, lh := mutterLogicalSize(w, h, c.Scale, c.Transform, layoutMode)
		rects[i] = displayRect{X: int(c.X), Y: int(c.Y), W: lw, H: lh}
		if c.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		return fmt.Errorf("%d primary logical monitors", primaries)
	}
	minX, minY := rects[0].X, rects[0].Y
	for _, r := range rects {
		minX, minY = min(minX, r.X), min(minY, r.Y)
	}
	if minX != 0 || minY != 0 {
		return errors.New("Logical monitors positions are offset")
	}
	for i, a := range rects {
		for _, b := range rects[i+1:] {
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				return errors.New("Logical monitors overlap")
			}
		}
	}
	reached := map[int]bool{0: true}
	for grew := true; grew; {
		grew = false
		for i := range rects {
			for j := range rects {
				if reached[i] && !reached[j] && fakeAdjacent(rects[i], rects[j]) {
					reached[j], grew = true, true
				}
			}
		}
	}
	if len(reached) != len(rects) {
		return errors.New("Logical monitors not adjacent")
	}
	return nil
}

// fakeAdjacent is mtk_rectangle_is_adjacent_to.
func fakeAdjacent(a, b displayRect) bool {
	if (a.X == b.X+b.W || a.X+a.W == b.X) && !(a.Y+a.H <= b.Y || a.Y >= b.Y+b.H) {
		return true
	}
	return (a.Y == b.Y+b.H || a.Y+a.H == b.Y) && !(a.X+a.W <= b.X || a.X >= b.X+b.W)
}

// showLocked makes configs the current configuration.
func (f *fakeMutter) showLocked(configs []mutterLogicalConfig, layoutMode uint32, leased map[string]bool) {
	for _, m := range f.monitors {
		m.current, m.forLease = "", leased[m.spec.Connector]
	}
	f.logical = nil
	for _, c := range configs {
		lm := mutterLogicalMonitor{X: int(c.X), Y: int(c.Y), Scale: c.Scale, Transform: c.Transform, Primary: c.Primary}
		for _, mc := range c.Monitors {
			m := f.monitorLocked(mc.Connector)
			m.current, m.props = mc.ModeID, copyFakeProps(mc.Props)
			lm.Monitors = append(lm.Monitors, m.spec)
		}
		f.logical = append(f.logical, lm)
	}
	f.layoutMode = layoutMode
	f.setCurrentLocked()
	f.changedLocked()
}

// changedLocked announces a new configuration (MonitorsChanged).
func (f *fakeMutter) changedLocked() {
	f.serial++
	for _, c := range f.casts {
		if !c.disconnected {
			select {
			case c.changed <- struct{}{}:
			default:
			}
		}
	}
}

// leavesVirtualOutLocked reports whether configs leave a virtual monitor
// out.
func (f *fakeMutter) leavesVirtualOutLocked(configs []mutterLogicalConfig) bool {
	configured := map[string]bool{}
	for _, c := range configs {
		for _, mc := range c.Monitors {
			configured[mc.Connector] = true
		}
	}
	for _, m := range f.monitors {
		if m.virtual && !configured[m.spec.Connector] {
			return true
		}
	}
	return false
}

// apply is ApplyMonitorsConfig with the temporary method. Properties a
// configuration does not pass are reset: monitor settings, leases and the
// layout mode.
func (f *fakeMutter) apply(c *fakeMutterCast, serial uint32, configs []mutterLogicalConfig, props map[string]dbus.Variant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.disconnected || f.crashed {
		return errors.New("ApplyMonitorsConfig: not connected")
	}
	if serial != f.serial {
		return errors.New("ApplyMonitorsConfig: The requested configuration is based on stale information")
	}
	layoutMode := f.defaultLayoutMode
	if v, ok := props["layout-mode"]; ok {
		if !f.canChangeLayoutMode {
			return errors.New("ApplyMonitorsConfig: Can't set layout mode")
		}
		layoutMode = v.Value().(uint32)
	}
	if err := f.verifyLocked(configs, layoutMode); err != nil {
		return fmt.Errorf("ApplyMonitorsConfig: %w", err)
	}
	if f.leavesVirtualOutLocked(configs) {
		f.crashed = true
		return errors.New("ApplyMonitorsConfig: GNOME Shell crashed")
	}
	leased := map[string]bool{}
	if v, ok := props["monitors-for-lease"]; ok {
		for _, spec := range v.Value().([]monitorSpec) {
			leased[spec.Connector] = true
		}
	}
	f.applies++
	f.showLocked(configs, layoutMode, leased)
	return nil
}

// configure applies and stores a configuration as Settings -> Displays
// does. modes gives monitors a new size (others keep theirs); monitors left
// out are switched off, leased ones stay leased.
func (f *fakeMutter) configure(t *testing.T, modes map[string][2]int, lms ...mutterLogicalMonitor) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var configs []mutterLogicalConfig
	for _, lm := range lms {
		c := mutterLogicalConfig{X: int32(lm.X), Y: int32(lm.Y), Scale: lm.Scale, Transform: lm.Transform, Primary: lm.Primary}
		for _, spec := range lm.Monitors {
			m := f.monitorLocked(spec.Connector)
			if m == nil {
				t.Fatalf("configure: no monitor %s", spec.Connector)
			}
			id := m.current
			if size, ok := modes[spec.Connector]; ok {
				id = fakeModeID(size[0], size[1])
			}
			c.Monitors = append(c.Monitors, mutterMonitorConfig{Connector: spec.Connector, ModeID: id, Props: m.props})
		}
		configs = append(configs, c)
	}
	if err := f.verifyLocked(configs, f.layoutMode); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if f.leavesVirtualOutLocked(configs) {
		t.Fatal("configure: GNOME Shell crashes on a configuration without its virtual monitor")
	}
	leased := map[string]bool{}
	for _, m := range f.monitors {
		leased[m.spec.Connector] = m.forLease
	}
	for _, c := range configs {
		for _, mc := range c.Monitors {
			leased[mc.Connector] = false
		}
	}
	f.showLocked(configs, f.layoutMode, leased)
	f.stored[f.current.key] = f.snapshotLocked()
}

// forgetStored drops the stored configurations, as when the desktop was
// arranged without storing it (e.g. by a tool applying a temporary
// configuration).
func (f *fakeMutter) forgetStored() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored = map[string]fakeMutterConfig{}
}

// plug connects a physical monitor.
func (f *fakeMutter) plug(m *fakeMutterMonitor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := 0
	for i < len(f.monitors) && !f.monitors[i].virtual {
		i++
	}
	f.monitors = append(f.monitors[:i:i], append([]*fakeMutterMonitor{m}, f.monitors[i:]...)...)
	f.relayoutLocked()
	f.changedLocked()
}

// switchOff switches a monitor off without any checks, as older GNOME
// Shell versions let the user do with a virtual monitor.
func (f *fakeMutter) switchOff(connector string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logical = mutterOtherLogical(mutterState{Logical: f.logical}, connector)
	f.monitorLocked(connector).current = ""
	f.setCurrentLocked()
	f.changedLocked()
}

// startCast is startMutterCast: RecordVirtual adds a virtual monitor that
// offers the requested modes, named and numbered as Mutter does. On a
// legacy fakeMutter it adds none, and startCast waits for the one a
// consumer asks for, as startMutterScreenCast does.
func (f *fakeMutter) startCast(w, h, nativeW, nativeH int) (mutterCast, mutterState, error) {
	f.mu.Lock()
	if f.legacy {
		c, before, err := f.startLegacyCastLocked()
		f.mu.Unlock()
		if err != nil {
			return nil, mutterState{}, err
		}
		spec, consumerSized, err := awaitNewMutterMonitor(c, before, w, h)
		if err != nil {
			c.Close()
			return nil, mutterState{}, err
		}
		f.mu.Lock()
		c.spec, c.consumerSized = spec, consumerSized
		f.mu.Unlock()
		return c, before, nil
	}
	defer f.mu.Unlock()
	if f.crashed {
		return nil, mutterState{}, errors.New("GNOME Shell crashed")
	}
	if f.startErr != nil {
		return nil, mutterState{}, f.startErr
	}
	before := f.stateLocked()
	m := f.newVirtualMonitorLocked()
	for _, props := range virtualMonitorModes(w, h, nativeW, nativeH) {
		size := props["size"].Value().(virtualMonitorModeSize)
		preferred, _ := props["is-preferred"].Value().(bool)
		m.modes = append(m.modes, fakeMutterModeOf(int(size.Width), int(size.Height), preferred))
	}
	f.monitors = append(f.monitors, m)
	c := &fakeMutterCast{
		f: f, spec: m.spec, node: uint32(40 + len(f.casts)),
		changed: make(chan struct{}, 1), closed: make(chan struct{}),
	}
	f.casts = append(f.casts, c)
	f.relayoutLocked()
	f.changedLocked()
	return c, before, nil
}

// newVirtualMonitorLocked names and numbers a virtual monitor as Mutter
// does.
func (f *fakeMutter) newVirtualMonitorLocked() *fakeMutterMonitor {
	n := 0
	for f.monitorLocked(fmt.Sprintf("Meta-%d", n)) != nil {
		n++
	}
	f.virtualSerial++
	return &fakeMutterMonitor{
		spec: monitorSpec{
			Connector: fmt.Sprintf("Meta-%d", n), Vendor: "MetaVendor",
			Product: "Virtual remote monitor", Serial: fmt.Sprintf("0x%06x", f.virtualSerial),
		},
		props:   map[string]dbus.Variant{},
		virtual: true,
	}
}

func (f *fakeMutter) startLegacyCastLocked() (*fakeMutterCast, mutterState, error) {
	if f.crashed {
		return nil, mutterState{}, errors.New("GNOME Shell crashed")
	}
	if f.startErr != nil {
		return nil, mutterState{}, f.startErr
	}
	c := &fakeMutterCast{
		f: f, node: uint32(40 + len(f.casts)),
		changed: make(chan struct{}, 1), closed: make(chan struct{}),
	}
	f.casts = append(f.casts, c)
	return c, f.stateLocked(), nil
}

// prime is primeMutterStream. On a legacy fakeMutter, as in Mutter 49's
// ensure_virtual_monitor, a consumer asking a stream for w x h creates its
// virtual monitor at that size, with that one mode, or changes the monitor
// to it, and Mutter lays the desktop out again; Mutter 50 ignores it.
func (f *fakeMutter) prime(node uint32, w, h int) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.primeErr != nil {
		return nil, f.primeErr
	}
	var c *fakeMutterCast
	for _, x := range f.casts {
		if x.node == node && !x.ended {
			c = x
		}
	}
	if c == nil {
		return nil, fmt.Errorf("no PipeWire node %d", node)
	}
	f.primes++
	stop := func() {}
	if !f.legacy {
		return stop, nil
	}
	mode := fakeMutterModeOf(w, h, true)
	if m := f.monitorLocked(c.spec.Connector); c.spec.Connector != "" && m != nil {
		if m.modes[0].ID == mode.ID {
			return stop, nil
		}
		m.modes = []mutterMode{mode}
	} else {
		m := f.newVirtualMonitorLocked()
		m.modes = []mutterMode{mode}
		f.monitors = append(f.monitors, m)
		c.spec = m.spec
	}
	f.relayoutLocked()
	f.changedLocked()
	return stop, nil
}

func (f *fakeMutter) primeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.primes
}

// endCastLocked ends a screen cast session, which removes its monitor.
func (f *fakeMutter) endCastLocked(c *fakeMutterCast) {
	if c.ended {
		return
	}
	c.ended = true
	var kept []*fakeMutterMonitor
	for _, m := range f.monitors {
		if m.spec != c.spec {
			kept = append(kept, m)
		}
	}
	f.monitors = kept
	f.relayoutLocked()
	f.changedLocked()
	close(c.closed)
}

// stopSharing ends a session from GNOME's screen-sharing indicator.
func (f *fakeMutter) stopSharing(c *fakeMutterCast) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endCastLocked(c)
}

func (f *fakeMutter) setFailState(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failState = err
}

// arrangement describes the desktop: each display as "connectors@x,y
// WxH", "*" marking the primary one.
func (f *fakeMutter) arrangement() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return describeMutterArrangement(f.stateLocked())
}

func describeMutterArrangement(st mutterState) string {
	var parts []string
	for _, lm := range st.Logical {
		var connectors []string
		for _, spec := range lm.Monitors {
			connectors = append(connectors, spec.Connector)
		}
		sort.Strings(connectors)
		r := st.rect(lm)
		part := fmt.Sprintf("%s@%d,%d %dx%d", strings.Join(connectors, "+"), r.X, r.Y, r.W, r.H)
		if lm.Primary {
			part += "*"
		}
		parts = append(parts, part)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func describeMutterConfigs(configs []mutterLogicalConfig) string {
	var parts []string
	for _, c := range configs {
		var connectors []string
		for _, m := range c.Monitors {
			connectors = append(connectors, m.Connector)
		}
		sort.Strings(connectors)
		part := fmt.Sprintf("%s@%d,%d", strings.Join(connectors, "+"), c.X, c.Y)
		if c.Primary {
			part += "*"
		}
		parts = append(parts, part)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func (f *fakeMutter) applyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applies
}

func (f *fakeMutter) castCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.casts)
}

func (f *fakeMutter) cast(i int) *fakeMutterCast {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.casts[i]
}

// monitor returns a copy of a monitor's state.
func (f *fakeMutter) monitor(connector string) (fakeMutterMonitor, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.monitorLocked(connector)
	if m == nil {
		return fakeMutterMonitor{}, false
	}
	copied := *m
	copied.props = copyFakeProps(m.props)
	return copied, true
}

func (f *fakeMutter) currentLayoutMode() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.layoutMode
}

func (f *fakeMutter) ended(c *fakeMutterCast) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return c.ended
}

// fakeMutterCast is a screen cast session on a fakeMutter.
type fakeMutterCast struct {
	f       *fakeMutter
	spec    monitorSpec
	node    uint32
	changed chan struct{}
	closed  chan struct{}
	// Guarded by f.mu.
	ended, disconnected, consumerSized bool
}

func (c *fakeMutterCast) ConsumerSized() bool {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	return c.consumerSized
}

func (c *fakeMutterCast) Monitor() monitorSpec     { return c.spec }
func (c *fakeMutterCast) Node() uint32             { return c.node }
func (c *fakeMutterCast) Closed() <-chan struct{}  { return c.closed }
func (c *fakeMutterCast) Changed() <-chan struct{} { return c.changed }

func (c *fakeMutterCast) State() (mutterState, error) {
	c.f.mu.Lock()
	disconnected := c.disconnected
	c.f.mu.Unlock()
	if disconnected {
		return mutterState{}, errors.New("GetCurrentState: not connected")
	}
	return c.f.state()
}

func (c *fakeMutterCast) Apply(serial uint32, logical []mutterLogicalConfig, props map[string]dbus.Variant) error {
	return c.f.apply(c, serial, logical, props)
}

func (c *fakeMutterCast) Stop() {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if !c.disconnected {
		c.f.endCastLocked(c)
	}
}

// Close disconnects from the bus; Mutter then ends the session.
func (c *fakeMutterCast) Close() {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.disconnected = true
	c.f.endCastLocked(c)
}

// --- session helpers ------------------------------------------------------------

type fakeMutterSession struct {
	mu     sync.Mutex
	mapped []monitorSpec // monitors the Drawsplay Pen was mapped to
}

func (s *fakeMutterSession) mappedTo() []monitorSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]monitorSpec(nil), s.mapped...)
}

// withFakeMutter points the GNOME virtual monitor at f, with a client whose
// native resolution is 2960x1848.
func withFakeMutter(t *testing.T, f *fakeMutter) *fakeMutterSession {
	t.Helper()
	s := &fakeMutterSession{}
	prevStart, prevSet := startMutterCast, setTabletOutput
	prevGrace, prevSettle := mutterReleaseGrace, mutterSettle
	prevAttempts, prevDelay := mutterLookupAttempts, mutterLookupDelay
	prevPrime, prevPrimeAttempts := primeMutterStream, mutterPrimeAttempts
	startMutterCast, primeMutterStream = f.startCast, f.prime
	setTabletOutput = func(c x11TabletCompositor, spec monitorSpec) error {
		if c.name != "Mutter" {
			t.Errorf("Drawsplay Pen mapped through %s, want Mutter", c.name)
		}
		s.mu.Lock()
		s.mapped = append(s.mapped, spec)
		s.mu.Unlock()
		return nil
	}
	mutterReleaseGrace, mutterSettle = time.Hour, time.Millisecond
	mutterLookupAttempts, mutterLookupDelay, mutterPrimeAttempts = 3, 0, 3
	resetMutterExt()

	const sessionID = 1 << 60
	bindSessionClientIdentity(sessionID, clientIdentity{Platform: "android", NativeWidth: 2960, NativeHeight: 1848})
	t.Cleanup(func() {
		unbindSessionClientIdentity(sessionID)
		resetMutterExt()
		startMutterCast, setTabletOutput = prevStart, prevSet
		mutterReleaseGrace, mutterSettle = prevGrace, prevSettle
		mutterLookupAttempts, mutterLookupDelay = prevAttempts, prevDelay
		primeMutterStream, mutterPrimeAttempts = prevPrime, prevPrimeAttempts
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.crashed {
			t.Error("a configuration left the virtual monitor out, which crashes GNOME Shell")
		}
	})
	return s
}

func resetMutterExt() {
	mutterExt.mu.Lock()
	defer mutterExt.mu.Unlock()
	stopMutterReleaseTimerLocked()
	mutterExt.gen, mutterExt.claimed, mutterExt.exiting = 0, false, false
	mutterExt.held, mutterExt.others = nil, ""
	mutterExt.seen, mutterExt.placement = nil, nil
}

func newTestMutterDisplay(t *testing.T, w, h int) *mutterDisplay {
	t.Helper()
	d, err := newMutterDisplay(videoOptions{Width: w, Height: h})
	if err != nil {
		t.Fatalf("newMutterDisplay: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func waitForMutterRestart(t *testing.T, d *mutterDisplay) {
	t.Helper()
	select {
	case <-d.watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not restart the session")
	}
}

func mutterWatching(d *mutterDisplay) bool {
	select {
	case <-d.watchDone:
		return false
	default:
		return true
	}
}

func heldMutterCast() mutterCast {
	mutterExt.mu.Lock()
	defer mutterExt.mu.Unlock()
	return mutterExt.held
}

func mutterReleasePending() bool {
	mutterExt.mu.Lock()
	defer mutterExt.mu.Unlock()
	return mutterExt.timer != nil
}

// expireMutterGrace does now what the release timer would do.
func expireMutterGrace(t *testing.T) {
	t.Helper()
	mutterExt.mu.Lock()
	defer mutterExt.mu.Unlock()
	if mutterExt.timer == nil {
		t.Fatal("the virtual monitor is not being released")
	}
	stopMutterReleaseTimerLocked()
	stopHeldMutterLocked()
}

func lastMutterPlacement() *x11Placement {
	mutterExt.mu.Lock()
	defer mutterExt.mu.Unlock()
	return mutterExt.placement
}

// --- modes and monitor state ------------------------------------------------------

func TestVirtualMonitorModesOfferTheConfiguredAndNativeResolutions(t *testing.T) {
	sizes := func(modes []map[string]dbus.Variant) (all, preferred []string) {
		for _, m := range modes {
			s := m["size"].Value().(virtualMonitorModeSize)
			name := fmt.Sprintf("%dx%d", s.Width, s.Height)
			all = append(all, name)
			if p, _ := m["is-preferred"].Value().(bool); p {
				preferred = append(preferred, name)
			}
		}
		return all, preferred
	}
	presets := []string{"1280x720", "1600x900", "1680x1050", "1920x1080", "2560x1440", "2736x1824", "2880x1800", "3200x1800", "3840x2160"}

	all, preferred := sizes(virtualMonitorModes(1366, 768, 2960, 1848))
	if want := append(append([]string{}, presets...), "1366x768", "2960x1848"); !reflect.DeepEqual(all, want) {
		t.Errorf("modes = %q, want %q", all, want)
	}
	if !reflect.DeepEqual(preferred, []string{"1366x768"}) {
		t.Errorf("preferred = %q, want the configured resolution", preferred)
	}

	all, preferred = sizes(virtualMonitorModes(1920, 1080, 0, 0))
	if !reflect.DeepEqual(all, presets) || !reflect.DeepEqual(preferred, []string{"1920x1080"}) {
		t.Errorf("without a native resolution: modes = %q, preferred = %q", all, preferred)
	}

	if sig := dbus.SignatureOf(virtualMonitorModes(1920, 1080, 0, 0)).String(); sig != "aa{sv}" {
		t.Errorf("modes signature = %s, want aa{sv}", sig)
	}
	if sig := dbus.SignatureOf(virtualMonitorModeSize{}).String(); sig != "(uu)" {
		t.Errorf("size signature = %s, want (uu)", sig)
	}
}

// roundTripMutterReply sends body through the D-Bus wire format as a method
// reply, as GetCurrentState's answer arrives.
func roundTripMutterReply(t *testing.T, body ...interface{}) []interface{} {
	t.Helper()
	msg := &dbus.Message{
		Type: dbus.TypeMethodReply,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldReplySerial: dbus.MakeVariant(uint32(1)),
			dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body...)),
		},
		Body: body,
	}
	var buf bytes.Buffer
	if err := msg.EncodeTo(&buf, binary.LittleEndian); err != nil {
		t.Fatalf("encoding the reply: %v", err)
	}
	decoded, err := dbus.DecodeMessage(&buf)
	if err != nil {
		t.Fatalf("decoding the reply: %v", err)
	}
	return decoded.Body
}

func TestParseMutterStateReadsGetCurrentState(t *testing.T) {
	type mode struct {
		ID                      string
		W, H                    int32
		Refresh, PreferredScale float64
		Scales                  []float64
		Props                   map[string]dbus.Variant
	}
	type monitor struct {
		Spec  monitorSpec
		Modes []mode
		Props map[string]dbus.Variant
	}
	type logical struct {
		X, Y      int32
		Scale     float64
		Transform uint32
		Primary   bool
		Monitors  []monitorSpec
		Props     map[string]dbus.Variant
	}
	v := dbus.MakeVariant
	panel := monitorSpec{Connector: "eDP-1", Vendor: "SDC", Product: "0x4171", Serial: "0x00000000"}
	headset := monitorSpec{Connector: "DP-3", Vendor: "VRH", Product: "Headset", Serial: "1"}
	body := []interface{}{
		uint32(7),
		[]monitor{
			{Spec: panel, Modes: []mode{
				{"2880x1800@60.000", 2880, 1800, 60, 2, []float64{1, 1.5, 2}, map[string]dbus.Variant{"is-current": v(true), "is-preferred": v(true)}},
				{"1920x1200@60.000", 1920, 1200, 60, 1, []float64{1, 1.25}, map[string]dbus.Variant{}},
			}, Props: map[string]dbus.Variant{
				"is-builtin": v(true), "display-name": v("Built-in display"),
				"is-underscanning": v(true), "color-mode": v(uint32(1)), "rgb-range": v(uint32(2)),
			}},
			{Spec: headset, Modes: []mode{
				{"2160x1200@90.000", 2160, 1200, 90, 1, []float64{1}, map[string]dbus.Variant{"is-preferred": v(true)}},
			}, Props: map[string]dbus.Variant{"is-for-lease": v(true)}},
		},
		[]logical{{Scale: 2, Primary: true, Monitors: []monitorSpec{panel}, Props: map[string]dbus.Variant{}}},
		map[string]dbus.Variant{
			"layout-mode": v(uint32(mutterLayoutLogical)), "supports-changing-layout-mode": v(true),
			"global-scale-required": v(false), "renderer": v("native"),
		},
	}
	if sig := dbus.SignatureOf(body...).String(); sig != "ua((ssss)a(siiddada{sv})a{sv})a(iiduba(ssss)a{sv})a{sv}" {
		t.Fatalf("test reply signature = %s", sig)
	}
	st, err := parseMutterState(roundTripMutterReply(t, body...))
	if err != nil {
		t.Fatalf("parseMutterState: %v", err)
	}
	want := mutterState{
		Serial: 7, LayoutMode: mutterLayoutLogical, CanChangeLayoutMode: true,
		Monitors: []mutterMonitor{
			{Spec: panel, Modes: []mutterMode{
				{ID: "2880x1800@60.000", W: 2880, H: 1800, PreferredScale: 2, Scales: []float64{1, 1.5, 2}, Current: true, Preferred: true},
				{ID: "1920x1200@60.000", W: 1920, H: 1200, PreferredScale: 1, Scales: []float64{1, 1.25}},
			}, Props: map[string]dbus.Variant{"underscanning": v(true), "color-mode": v(uint32(1)), "rgb-range": v(uint32(2))}},
			{Spec: headset, Modes: []mutterMode{
				{ID: "2160x1200@90.000", W: 2160, H: 1200, PreferredScale: 1, Scales: []float64{1}, Preferred: true},
			}, Props: map[string]dbus.Variant{}, ForLease: true},
		},
		Logical: []mutterLogicalMonitor{{Scale: 2, Primary: true, Monitors: []monitorSpec{panel}}},
	}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("parseMutterState =\n%+v\nwant\n%+v", st, want)
	}

	st, err = parseMutterState(roundTripMutterReply(t, uint32(3), []monitor{}, []logical{}, map[string]dbus.Variant{}))
	if err != nil {
		t.Fatalf("parseMutterState without properties: %v", err)
	}
	if st.Serial != 3 || st.LayoutMode != mutterLayoutPhysical || st.CanChangeLayoutMode || st.GlobalScale {
		t.Errorf("without properties: %+v, want the physical layout mode", st)
	}

	if _, err := parseMutterState([]interface{}{uint32(1)}); err == nil {
		t.Error("parseMutterState accepted a short reply")
	}
}

func TestMutterLogicalSize(t *testing.T) {
	tests := []struct {
		w, h       int
		scale      float64
		transform  uint32
		layoutMode uint32
		wantW      int
		wantH      int
	}{
		{1920, 1080, 1, 0, mutterLayoutPhysical, 1920, 1080},
		{2880, 1800, 2, 0, mutterLayoutPhysical, 2880, 1800},
		{2880, 1800, 2, 0, mutterLayoutLogical, 1440, 900},
		{2560, 1600, 1.5, 0, mutterLayoutLogical, 1707, 1067},
		{1920, 1080, 1, 1, mutterLayoutPhysical, 1080, 1920},
		{1920, 1080, 1, 2, mutterLayoutPhysical, 1920, 1080},
		{1920, 1080, 1, 3, mutterLayoutPhysical, 1080, 1920},
		{1920, 1080, 1, 4, mutterLayoutPhysical, 1920, 1080},
		{1920, 1080, 1, 5, mutterLayoutPhysical, 1080, 1920},
		{2560, 1600, 1.5, 1, mutterLayoutLogical, 1067, 1707},
	}
	for _, tt := range tests {
		w, h := mutterLogicalSize(tt.w, tt.h, tt.scale, tt.transform, tt.layoutMode)
		if w != tt.wantW || h != tt.wantH {
			t.Errorf("mutterLogicalSize(%d, %d, %v, %d, %d) = %dx%d, want %dx%d",
				tt.w, tt.h, tt.scale, tt.transform, tt.layoutMode, w, h, tt.wantW, tt.wantH)
		}
	}
}

func TestMutterLayoutValid(t *testing.T) {
	r := func(x, y int) displayRect { return displayRect{X: x, Y: y, W: 100, H: 100} }
	tests := []struct {
		name  string
		rects []displayRect
		want  bool
	}{
		{"none", nil, true},
		{"one", []displayRect{r(0, 0)}, true},
		{"side by side", []displayRect{r(0, 0), r(100, 0)}, true},
		{"corners touching", []displayRect{r(0, 0), r(100, 100)}, false},
		{"one pixel of edge", []displayRect{r(0, 0), r(100, 99)}, true},
		{"apart", []displayRect{r(0, 0), r(101, 0)}, false},
		{"chain", []displayRect{r(0, 0), r(100, 0), r(200, 0)}, true},
		{"two separate pairs", []displayRect{r(0, 0), r(100, 0), r(0, 200), r(100, 200)}, false},
		{"overlap", []displayRect{r(0, 0), r(50, 0)}, false},
	}
	for _, tt := range tests {
		if got := mutterLayoutValid(tt.rects); got != tt.want {
			t.Errorf("%s: mutterLayoutValid = %t, want %t", tt.name, got, tt.want)
		}
	}
}

func TestMutterPickScale(t *testing.T) {
	mode := func(preferred float64, scales ...float64) mutterMode {
		return mutterMode{W: 2560, H: 1600, PreferredScale: preferred, Scales: scales}
	}
	others := []mutterLogicalMonitor{{Scale: 2}}
	tests := []struct {
		name       string
		mode       mutterMode
		current    float64
		others     []mutterLogicalMonitor
		global     bool
		layoutMode uint32
		want       float64
	}{
		{"keeps the current scale", mode(1, 1, 1.25, 1.5, 2), 1.5, nil, false, mutterLayoutLogical, 1.5},
		{"a new monitor gets the preferred one", mode(2, 1, 1.25, 1.5, 2), 0, nil, false, mutterLayoutLogical, 2},
		{"Mutter's own value", mode(1, 1, 1.2500000476837158, 2), 1.25, nil, false, mutterLayoutLogical, 1.2500000476837158},
		{"the first supported one", mode(1.5, 2, 3), 0, nil, false, mutterLayoutLogical, 2},
		{"a global scale", mode(1, 1, 2), 1, others, true, mutterLayoutLogical, 2},
		{"whole scales in the physical layout mode", mode(1.5, 1, 1.5, 2), 1.5, nil, false, mutterLayoutPhysical, 1},
	}
	for _, tt := range tests {
		if got := mutterPickScale(tt.mode, tt.current, tt.others, tt.global, tt.layoutMode); got != tt.want {
			t.Errorf("%s: mutterPickScale = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// --- layout -------------------------------------------------------------------------

func TestPlanMutterLayout(t *testing.T) {
	desk := func(t *testing.T) *fakeMutter { return newFakeDeskMutter(t) }
	tests := []struct {
		name string
		// setup returns the fake, the screen cast that added the virtual
		// monitor and the state before it was added (nil once in use).
		setup      func(t *testing.T) (*fakeMutter, mutterCast, *mutterState)
		size       [2]int
		remembered *x11Placement
		wantKeep   bool
		wantRect   displayRect
		fromBefore bool
		want       string
	}{
		{
			name:       "a new monitor goes right of the desktop as the user arranged it",
			setup:      fakeMutterWithCast(desk, 1920, 1080, nil),
			size:       [2]int{1920, 1080},
			wantRect:   displayRect{X: 2560, Y: 0, W: 1920, H: 1080},
			fromBefore: true,
			want:       "HDMI-1@0,0 2560x1440 Meta-0@2560,0 1920x1080 eDP-1@320,1440 1920x1080*",
		},
		{
			name:       "a new monitor returns to where the last one was",
			setup:      fakeMutterWithCast(desk, 1920, 1080, nil),
			size:       [2]int{1920, 1080},
			remembered: &x11Placement{Side: "below", Offset: 500},
			wantRect:   displayRect{X: 500, Y: 2520, W: 1920, H: 1080},
			fromBefore: true,
			want:       "HDMI-1@0,0 2560x1440 Meta-0@500,2520 1920x1080 eDP-1@320,1440 1920x1080*",
		},
		{
			name: "a new monitor slides along its side until it touches a display",
			setup: fakeMutterWithCast(func(t *testing.T) *fakeMutter {
				f := newFakeMutter(fakeLaptopPanel(), fakeExternalMonitor())
				f.configure(t, nil, fakeLM(0, 0, false, "HDMI-1"), fakeLM(2560, 0, true, "eDP-1"))
				return f
			}, 1920, 1080, nil),
			size:       [2]int{1920, 1080},
			remembered: &x11Placement{Side: "right", Offset: 1200},
			wantRect:   displayRect{X: 4480, Y: 1079, W: 1920, H: 1080},
			fromBefore: true,
			want:       "HDMI-1@0,0 2560x1440 Meta-0@4480,1079 1920x1080 eDP-1@2560,0 1920x1080*",
		},
		{
			name: "monitors plugged in meanwhile keep GNOME's layout",
			setup: func(t *testing.T) (*fakeMutter, mutterCast, *mutterState) {
				f := newFakeLaptopMutter()
				st0, _ := f.state()
				f.plug(fakeExternalMonitor())
				c, _, err := f.startCast(1920, 1080, 2960, 1848)
				if err != nil {
					t.Fatal(err)
				}
				return f, c, &st0
			},
			size:     [2]int{1920, 1080},
			wantKeep: true,
			wantRect: displayRect{X: 4480, Y: 0, W: 1920, H: 1080},
			want:     "HDMI-1@1920,0 2560x1440 Meta-0@4480,0 1920x1080 eDP-1@0,0 1920x1080*",
		},
		{
			name: "a monitor showing the size stays where the user put it",
			setup: fakeMutterWithCast(newFakeLaptop, 1920, 1080, func(t *testing.T, f *fakeMutter) {
				f.configure(t, nil, fakeLM(0, 0, true, "eDP-1"), fakeLM(0, 1080, false, "Meta-0"))
			}),
			size:     [2]int{1920, 1080},
			wantKeep: true,
			wantRect: displayRect{X: 0, Y: 1080, W: 1920, H: 1080},
			want:     "Meta-0@0,1080 1920x1080 eDP-1@0,0 1920x1080*",
		},
		{
			name: "a new size keeps the monitor's side of the desktop",
			setup: fakeMutterWithCast(newFakeLaptop, 1920, 1080, func(t *testing.T, f *fakeMutter) {
				f.configure(t, nil, fakeLM(0, 0, false, "Meta-0"), fakeLM(1920, 0, true, "eDP-1"))
			}),
			size:     [2]int{2560, 1440},
			wantRect: displayRect{X: 0, Y: 0, W: 2560, H: 1440},
			want:     "Meta-0@0,0 2560x1440 eDP-1@2560,0 1920x1080*",
		},
		{
			name: "a mirror at the size stays a mirror",
			setup: fakeMutterWithCast(newFakeLaptop, 1920, 1080, func(t *testing.T, f *fakeMutter) {
				f.configure(t, nil, fakeLM(0, 0, true, "eDP-1", "Meta-0"))
			}),
			size:     [2]int{1920, 1080},
			wantKeep: true,
			wantRect: displayRect{X: 0, Y: 0, W: 1920, H: 1080},
			want:     "Meta-0+eDP-1@0,0 1920x1080*",
		},
		{
			name: "a mirror at another size extends the desktop",
			setup: fakeMutterWithCast(newFakeLaptop, 1920, 1080, func(t *testing.T, f *fakeMutter) {
				f.configure(t, nil, fakeLM(0, 0, true, "eDP-1", "Meta-0"))
			}),
			size:     [2]int{2560, 1440},
			wantRect: displayRect{X: 1920, Y: 0, W: 2560, H: 1440},
			want:     "Meta-0@1920,0 2560x1440 eDP-1@0,0 1920x1080*",
		},
		{
			name: "the primary display stays primary",
			setup: fakeMutterWithCast(newFakeLaptop, 1920, 1080, func(t *testing.T, f *fakeMutter) {
				f.configure(t, nil, fakeLM(0, 0, false, "eDP-1"), fakeLM(1920, 0, true, "Meta-0"))
			}),
			size:     [2]int{2560, 1440},
			wantRect: displayRect{X: 1920, Y: 0, W: 2560, H: 1440},
			want:     "Meta-0@1920,0 2560x1440* eDP-1@0,0 1920x1080",
		},
		{
			name: "a monitor between two displays changes size where it is",
			setup: fakeMutterWithCast(newFakeDeskMonitors, 1920, 1200, func(t *testing.T, f *fakeMutter) {
				f.configure(t, map[string][2]int{"Meta-0": {1920, 1080}},
					fakeLM(0, 0, true, "eDP-1"), fakeLM(1920, 0, false, "Meta-0"), fakeLM(3840, 0, false, "HDMI-1"))
			}),
			size:     [2]int{1920, 1200},
			wantRect: displayRect{X: 1920, Y: 0, W: 1920, H: 1200},
			want:     "HDMI-1@3840,0 2560x1440 Meta-0@1920,0 1920x1200 eDP-1@0,0 1920x1080*",
		},
		{
			name:       "the only display",
			setup:      fakeMutterWithCast(func(*testing.T) *fakeMutter { return newFakeMutter() }, 1920, 1080, nil),
			size:       [2]int{1920, 1080},
			wantKeep:   true,
			wantRect:   displayRect{X: 0, Y: 0, W: 1920, H: 1080},
			fromBefore: false, // there are no other monitors
			want:       "Meta-0@0,0 1920x1080*",
		},
		{
			name: "scaled displays in the logical layout mode",
			setup: fakeMutterWithCast(func(*testing.T) *fakeMutter {
				return newFakeMutter(fakeHiDPIPanel()).inLogicalLayoutMode()
			}, 1920, 1080, nil),
			size:       [2]int{1920, 1080},
			wantKeep:   true,
			wantRect:   displayRect{X: 1440, Y: 0, W: 1920, H: 1080},
			fromBefore: true,
			want:       "Meta-0@1440,0 1920x1080 eDP-1@0,0 1440x900*",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, c, before := tt.setup(t)
			st, err := c.State()
			if err != nil {
				t.Fatal(err)
			}
			layout, err := planMutterLayout(st, c.Monitor().Connector, tt.size[0], tt.size[1], tt.remembered, before)
			if err != nil {
				t.Fatalf("planMutterLayout: %v", err)
			}
			if layout.Keep != tt.wantKeep || layout.Rect != tt.wantRect || (layout.Others != "") != tt.fromBefore {
				t.Errorf("Keep = %t, Rect = %+v, Others = %q; want Keep = %t, Rect = %+v, from before = %t",
					layout.Keep, layout.Rect, layout.Others, tt.wantKeep, tt.wantRect, tt.fromBefore)
			}
			if !layout.Keep {
				if err := c.Apply(st.Serial, layout.Configs, layout.Props); err != nil {
					t.Fatalf("Mutter refused the layout %s: %v", describeMutterConfigs(layout.Configs), err)
				}
			}
			if got := f.arrangement(); got != tt.want {
				t.Errorf("arrangement = %q, want %q", got, tt.want)
			}
		})
	}
}

func newFakeLaptop(*testing.T) *fakeMutter { return newFakeLaptopMutter() }

// newFakeDeskMonitors has newFakeDeskMutter's monitors as GNOME first lays
// them out.
func newFakeDeskMonitors(*testing.T) *fakeMutter {
	return newFakeMutter(fakeLaptopPanel(), fakeExternalMonitor())
}

// fakeMutterWithCast builds a planMutterLayout setup: the fake from
// newFake, a virtual monitor created at w x h, and then arrange (if any) as
// the user's changes. The state before the monitor was added is passed on
// only without arrange; a monitor the user arranged has been in use.
func fakeMutterWithCast(newFake func(*testing.T) *fakeMutter, w, h int, arrange func(*testing.T, *fakeMutter)) func(*testing.T) (*fakeMutter, mutterCast, *mutterState) {
	return func(t *testing.T) (*fakeMutter, mutterCast, *mutterState) {
		t.Helper()
		f := newFake(t)
		c, before, err := f.startCast(w, h, 2960, 1848)
		if err != nil {
			t.Fatal(err)
		}
		if arrange != nil {
			arrange(t, f)
			return f, c, nil
		}
		return f, c, &before
	}
}

func TestPlanMutterLayoutErrors(t *testing.T) {
	f := newFakeLaptopMutter()
	c, _, err := f.startCast(1920, 1080, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := c.State()
	if _, err := planMutterLayout(st, "Meta-0", 1366, 768, nil, nil); !errors.Is(err, errMutterModeMissing) {
		t.Errorf("a size the monitor does not offer: err = %v, want errMutterModeMissing", err)
	}
	if _, err := planMutterLayout(st, "Meta-7", 1920, 1080, nil, nil); err == nil {
		t.Error("planned a monitor Mutter does not list")
	}

	_, c, _ = fakeMutterWithCast(newFakeDeskMonitors, 1920, 1200, func(t *testing.T, f *fakeMutter) {
		f.configure(t, map[string][2]int{"Meta-0": {1920, 1080}},
			fakeLM(0, 0, true, "eDP-1"), fakeLM(1920, 0, false, "Meta-0"), fakeLM(3840, 0, false, "HDMI-1"))
	})(t)
	st, _ = c.State()
	if layout, err := planMutterLayout(st, "Meta-0", 2560, 1440, nil, nil); err == nil {
		t.Errorf("planned %s for a monitor too wide for its place between two displays", describeMutterConfigs(layout.Configs))
	}
}

func TestPlanMutterRelease(t *testing.T) {
	tests := []struct {
		name    string
		desktop []mutterLogicalMonitor
		want    string // "" for none
	}{
		{
			name: "the others move back to the desktop origin; one takes over the primary role",
			desktop: []mutterLogicalMonitor{
				fakeLM(0, 0, true, "Meta-0"), fakeLM(0, 1080, false, "eDP-1"), fakeLM(0, 2160, false, "HDMI-1"),
			},
			want: "HDMI-1@0,1080 eDP-1@0,0*",
		},
		{
			name:    "a mirrored display stays",
			desktop: []mutterLogicalMonitor{fakeLM(0, 0, true, "eDP-1", "Meta-0"), fakeLM(1920, 0, false, "HDMI-1")},
			want:    "HDMI-1@1920,0 eDP-1@0,0*",
		},
		{
			name: "displays that would not touch are left to GNOME",
			desktop: []mutterLogicalMonitor{
				fakeLM(0, 0, true, "eDP-1"), fakeLM(1920, 0, false, "Meta-0"), fakeLM(3840, 0, false, "HDMI-1"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeMutter(fakeLaptopPanel(), fakeExternalMonitor())
			if _, _, err := f.startCast(1920, 1080, 0, 0); err != nil {
				t.Fatal(err)
			}
			f.configure(t, nil, tt.desktop...)
			st, _ := f.state()
			if got := describeMutterConfigs(planMutterRelease(st, "Meta-0")); got != tt.want {
				t.Errorf("planMutterRelease = %q, want %q", got, tt.want)
			}
		})
	}

	f := newFakeMutter()
	if _, _, err := f.startCast(1920, 1080, 0, 0); err != nil {
		t.Fatal(err)
	}
	st, _ := f.state()
	if got := planMutterRelease(st, "Meta-0"); got != nil {
		t.Errorf("without other monitors: planMutterRelease = %s, want none", describeMutterConfigs(got))
	}
}

func TestObserveMutterMonitor(t *testing.T) {
	f := newFakeLaptopMutter()
	if _, _, err := f.startCast(2560, 1440, 2960, 1848); err != nil {
		t.Fatal(err)
	}
	st, _ := f.state()
	obs := observeMutterMonitor(st, "Meta-0")
	if !obs.Present || !obs.Enabled || obs.Rect != (displayRect{X: 1920, Y: 0, W: 2560, H: 1440}) ||
		obs.ModeW != 2560 || obs.ModeH != 1440 || !obs.AutoMode || obs.Mirrored ||
		!obs.Placed || obs.Placement != (x11Placement{Side: "right"}) {
		t.Errorf("as GNOME added it: %+v", obs)
	}

	f.configure(t, map[string][2]int{"Meta-0": {1920, 1080}}, fakeLM(0, 0, true, "eDP-1", "Meta-0"))
	st, _ = f.state()
	if obs := observeMutterMonitor(st, "Meta-0"); !obs.Mirrored || obs.Placed || obs.AutoMode || obs.ModeW != 1920 {
		t.Errorf("mirroring the laptop panel: %+v", obs)
	}

	f.switchOff("Meta-0")
	st, _ = f.state()
	if obs := observeMutterMonitor(st, "Meta-0"); !obs.Present || obs.Enabled {
		t.Errorf("switched off: %+v", obs)
	}
	if obs := observeMutterMonitor(st, "Meta-9"); obs.Present {
		t.Errorf("an unknown monitor: %+v", obs)
	}
}

// scriptedMutterCast answers State with states in turn, then the last one.
type scriptedMutterCast struct {
	mutterCast
	states []mutterState
	calls  int
}

func (c *scriptedMutterCast) State() (mutterState, error) {
	st := c.states[min(c.calls, len(c.states)-1)]
	c.calls++
	return st, nil
}

// namedMutterCast is a cast of a given monitor and PipeWire node.
type namedMutterCast struct {
	mutterCast
	spec          monitorSpec
	node          uint32
	consumerSized bool
}

func (c namedMutterCast) Monitor() monitorSpec { return c.spec }
func (c namedMutterCast) Node() uint32         { return c.node }
func (c namedMutterCast) ConsumerSized() bool  { return c.consumerSized }

func TestFindNewMutterMonitor(t *testing.T) {
	prevDelay := mutterLookupDelay
	mutterLookupDelay = 0
	t.Cleanup(func() { mutterLookupDelay = prevDelay })

	f := newFakeLaptopMutter()
	before, _ := f.state()
	for _, size := range [][2]int{{1280, 720}, {1920, 1080}} {
		if _, _, err := f.startCast(size[0], size[1], 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := f.state()

	c := &scriptedMutterCast{states: []mutterState{before, st}}
	if spec, err := findNewMutterMonitor(c, before, 1920, 1080, 3); err != nil || spec.Connector != "Meta-1" || c.calls != 2 {
		t.Errorf("findNewMutterMonitor = %v, %v after %d calls; want Meta-1, the one created at 1920x1080, once Mutter lists it", spec, err, c.calls)
	}
	c = &scriptedMutterCast{states: []mutterState{st}}
	if spec, err := findNewMutterMonitor(c, before, 3840, 2160, 3); err != nil || spec.Connector != "Meta-0" {
		t.Errorf("findNewMutterMonitor = %v, %v; want the first new monitor", spec, err)
	}
	c = &scriptedMutterCast{states: []mutterState{before}}
	if spec, err := findNewMutterMonitor(c, before, 1920, 1080, 3); !errors.Is(err, errMutterMonitorNotListed) || c.calls != 3 {
		t.Errorf("findNewMutterMonitor = %v, %v after %d calls; want an error after 3", spec, err, c.calls)
	}
}

func TestMutterTarget(t *testing.T) {
	f := newFakeLaptopMutter()
	if _, _, err := f.startCast(1365, 767, 0, 0); err != nil {
		t.Fatal(err)
	}
	st, _ := f.state()
	meta0 := namedMutterCast{spec: monitorSpec{Connector: "Meta-0"}, node: 40}
	got, err := mutterTarget(st, meta0)
	want := captureTarget{
		Kind: capturePipeWire, Node: 40, Width: 1364, Height: 766,
		Rect: displayRect{X: 1920, Y: 0, W: 1365, H: 767}, TabletMapped: true,
	}
	if err != nil || got != want {
		t.Errorf("mutterTarget = %+v, %v; want %+v", got, err, want)
	}
	legacy := meta0
	legacy.consumerSized = true
	want.SizeFromConsumer, want.HostResolution = true, true
	if got, err := mutterTarget(st, legacy); err != nil || got != want {
		t.Errorf("a consumer-sized monitor: mutterTarget = %+v, %v; want %+v", got, err, want)
	}
	if _, err := mutterTarget(st, namedMutterCast{spec: monitorSpec{Connector: "Meta-9"}, node: 40}); err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Errorf("an unknown monitor: err = %v", err)
	}
	f.switchOff("Meta-0")
	st, _ = f.state()
	if _, err := mutterTarget(st, meta0); err == nil || !strings.Contains(err.Error(), "is disabled") {
		t.Errorf("a monitor switched off: err = %v", err)
	}
}

// --- the virtual monitor across sessions ---------------------------------------------

func TestMutterVirtualMonitorExtendsTheDesktop(t *testing.T) {
	f := newFakeDeskMutter(t)
	s := withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)

	if got, want := f.arrangement(), "HDMI-1@0,0 2560x1440 Meta-0@2560,0 1920x1080 eDP-1@320,1440 1920x1080*"; got != want {
		t.Errorf("arrangement = %q, want %q", got, want)
	}
	want := captureTarget{
		Kind: capturePipeWire, Node: 40, Width: 1920, Height: 1080,
		Rect: displayRect{X: 2560, Y: 0, W: 1920, H: 1080}, TabletMapped: true,
	}
	if got := d.Target(); got != want {
		t.Errorf("Target = %+v, want %+v", got, want)
	}
	if got := s.mappedTo(); !reflect.DeepEqual(got, []monitorSpec{f.cast(0).Monitor()}) {
		t.Errorf("Drawsplay Pen mapped to %v, want the virtual monitor", got)
	}
	if got, want := d.Description(), "GNOME virtual monitor Meta-0 (PipeWire node 40, 1920x1080 at +2560+0)"; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
	if mode, kind := displayModeFromTarget(d.Target()); mode != "extend" || kind != "pipewire" {
		t.Errorf("display mode = %s (%s), want extend (pipewire)", mode, kind)
	}
	if n := f.applyCount(); n != 1 {
		t.Errorf("%d configurations applied, want 1", n)
	}
	if m, _ := f.monitor("Meta-0"); !fakeOffers(m, 2960, 1848) {
		t.Error("the virtual monitor does not offer the tablet's native resolution")
	}
}

func fakeOffers(m fakeMutterMonitor, w, h int) bool {
	_, ok := m.modeByID(fakeModeID(w, h))
	return ok
}

// fakeMutterWithSettings has a scaled laptop panel, a TV with underscanning
// and a leased VR headset, in the logical layout mode, arranged by the user
// (stored, or applied without storing it).
func fakeMutterWithSettings(t *testing.T, stored bool) *fakeMutter {
	tv := fakePhysicalMonitor("HDMI-1", "GSM", "LG TV", "0x01010101", [2]int{1920, 1080})
	tv.props["underscanning"] = dbus.MakeVariant(true)
	headset := fakePhysicalMonitor("DP-3", "VRH", "Headset", "1", [2]int{2160, 1200})
	f := newFakeMutter(fakeHiDPIPanel(), tv, headset).inLogicalLayoutMode()
	f.mu.Lock()
	headset.forLease = true
	f.mu.Unlock()
	panel := fakeLM(0, 0, true, "eDP-1")
	panel.Scale = 2
	f.configure(t, nil, panel, fakeLM(1440, 0, false, "HDMI-1"))
	if !stored {
		f.forgetStored()
	}
	return f
}

func checkFakeMutterSettings(t *testing.T, f *fakeMutter, when string) {
	t.Helper()
	if m, _ := f.monitor("HDMI-1"); m.props["underscanning"].Value() != true {
		t.Errorf("%s: the TV's underscanning was turned off", when)
	}
	if m, _ := f.monitor("DP-3"); !m.forLease || m.current != "" {
		t.Errorf("%s: the VR headset is no longer leased out", when)
	}
	if f.currentLayoutMode() != mutterLayoutLogical {
		t.Errorf("%s: layout mode = %d, want logical", when, f.currentLayoutMode())
	}
}

// GNOME lays the desktop out again for the new monitor, which ends leases;
// they are restored with the arrangement.
func TestMutterVirtualMonitorKeepsTheOtherDisplaysSettings(t *testing.T) {
	for _, stored := range []bool{true, false} {
		t.Run(fmt.Sprintf("stored=%t", stored), func(t *testing.T) {
			f := fakeMutterWithSettings(t, stored)
			withFakeMutter(t, f)
			d := newTestMutterDisplay(t, 1920, 1080)
			if got, want := f.arrangement(), "HDMI-1@1440,0 1920x1080 Meta-0@3360,0 1920x1080 eDP-1@0,0 1440x900*"; got != want {
				t.Errorf("arrangement = %q, want %q", got, want)
			}
			checkFakeMutterSettings(t, f, "with the virtual monitor")

			d.Close()
			expireMutterGrace(t)
			if got, want := f.arrangement(), "HDMI-1@1440,0 1920x1080 eDP-1@0,0 1440x900*"; got != want {
				t.Errorf("after the virtual monitor: arrangement = %q, want %q", got, want)
			}
			checkFakeMutterSettings(t, f, "after the virtual monitor")
			// GNOME itself restores a stored arrangement.
			if n, want := f.applyCount(), map[bool]int{true: 1, false: 2}[stored]; n != want {
				t.Errorf("%d configurations applied, want %d", n, want)
			}
		})
	}
}

func TestMutterVirtualMonitorKeepsTheLayoutMode(t *testing.T) {
	for _, stored := range []bool{true, false} {
		t.Run(fmt.Sprintf("stored=%t", stored), func(t *testing.T) {
			// The logical layout mode is GNOME's default; the user's
			// arrangement is in the physical one.
			tv := fakePhysicalMonitor("HDMI-1", "GSM", "LG TV", "0x01010101", [2]int{1920, 1080})
			f := newFakeMutter(fakeHiDPIPanel(), tv).inLogicalLayoutMode()
			f.mu.Lock()
			f.layoutMode = mutterLayoutPhysical
			f.mu.Unlock()
			panel := fakeLM(0, 0, true, "eDP-1")
			panel.Scale = 2
			f.configure(t, nil, panel, fakeLM(2880, 0, false, "HDMI-1"))
			if !stored {
				f.forgetStored()
			}
			withFakeMutter(t, f)
			d := newTestMutterDisplay(t, 1920, 1080)
			if got, want := f.arrangement(), "HDMI-1@2880,0 1920x1080 Meta-0@4800,0 1920x1080 eDP-1@0,0 2880x1800*"; got != want {
				t.Errorf("arrangement = %q, want %q", got, want)
			}
			if f.currentLayoutMode() != mutterLayoutPhysical {
				t.Error("the layout mode changed to GNOME's default")
			}

			d.Close()
			expireMutterGrace(t)
			if got, want := f.arrangement(), "HDMI-1@2880,0 1920x1080 eDP-1@0,0 2880x1800*"; got != want {
				t.Errorf("after the virtual monitor: arrangement = %q, want %q", got, want)
			}
			if f.currentLayoutMode() != mutterLayoutPhysical {
				t.Error("after the virtual monitor: the layout mode changed to GNOME's default")
			}
		})
	}
}

func TestMutterVirtualMonitorSurvivesAQuickReconnect(t *testing.T) {
	f := newFakeDeskMutter(t)
	s := withFakeMutter(t, f)
	d1 := newTestMutterDisplay(t, 1920, 1080)
	first := d1.Target()
	d1.Close()
	if !mutterReleasePending() {
		t.Fatal("closing the session did not start the grace period")
	}

	d2 := newTestMutterDisplay(t, 1920, 1080)
	if f.castCount() != 1 || f.applyCount() != 1 {
		t.Errorf("%d monitors created and %d configurations applied, want 1 and 1", f.castCount(), f.applyCount())
	}
	if d2.Target() != first {
		t.Errorf("Target = %+v, want %+v", d2.Target(), first)
	}
	mutterExt.mu.Lock()
	claimed := mutterExt.claimed
	mutterExt.mu.Unlock()
	if !claimed || mutterReleasePending() {
		t.Error("the new session did not claim the virtual monitor")
	}
	if n := len(s.mappedTo()); n != 2 {
		t.Errorf("Drawsplay Pen mapped %d times, want once per session", n)
	}
}

func TestMutterVirtualMonitorGoesAfterTheGracePeriod(t *testing.T) {
	f := newFakeDeskMutter(t)
	withFakeMutter(t, f)
	mutterReleaseGrace = time.Millisecond
	d := newTestMutterDisplay(t, 1920, 1080)
	d.Close()
	waitUntil(t, "the virtual monitor to go", func() bool { return heldMutterCast() == nil })

	if _, ok := f.monitor("Meta-0"); ok {
		t.Error("the virtual monitor is still there")
	}
	if got := f.arrangement(); got != fakeDeskArrangement {
		t.Errorf("arrangement = %q, want %q", got, fakeDeskArrangement)
	}
	if n := f.applyCount(); n != 1 {
		t.Errorf("%d configurations applied, want 1 (GNOME restores the stored arrangement)", n)
	}
	if p := lastMutterPlacement(); p == nil || *p != (x11Placement{Side: "right"}) {
		t.Errorf("remembered placement = %v, want right", p)
	}
}

// Like the X11 extended display, the other displays stay where the user put
// them while the virtual monitor was there.
func TestMutterVirtualMonitorLeavesTheOtherDisplaysWhereTheUserPutThem(t *testing.T) {
	f := newFakeDeskMutter(t)
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	f.configure(t, nil, fakeLM(0, 0, true, "eDP-1"), fakeLM(1920, 0, false, "HDMI-1"), fakeLM(4480, 0, false, "Meta-0"))
	waitUntil(t, "the move to be seen", func() bool { return d.Target().Rect.X == 4480 })

	d.Close()
	expireMutterGrace(t)
	if got, want := f.arrangement(), "HDMI-1@1920,0 2560x1440 eDP-1@0,0 1920x1080*"; got != want {
		t.Errorf("arrangement = %q, want %q", got, want)
	}
	if n := f.applyCount(); n != 2 {
		t.Errorf("%d configurations applied, want 2", n)
	}
}

func TestMutterVirtualMonitorFollowsAHotplug(t *testing.T) {
	f := newFakeDeskMutter(t)
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	f.plug(fakePhysicalMonitor("DP-2", "GSM", "27GL850", "0x00000456", [2]int{1920, 1080}))
	waitUntil(t, "the virtual monitor's new place", func() bool { return d.Target().Rect.X == 6400 })
	if !mutterWatching(d) {
		t.Error("a hotplug at the same size restarted the session")
	}

	// The others are left as GNOME arranged them for the new monitor.
	d.Close()
	expireMutterGrace(t)
	if got, want := f.arrangement(), "DP-2@4480,0 1920x1080 HDMI-1@1920,0 2560x1440 eDP-1@0,0 1920x1080*"; got != want {
		t.Errorf("arrangement = %q, want %q", got, want)
	}
	if n := f.applyCount(); n != 1 {
		t.Errorf("%d configurations applied, want 1", n)
	}
}

func TestMutterVirtualMonitorComesBackWhereTheUserPutIt(t *testing.T) {
	f := newFakeLaptopMutter()
	s := withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	f.configure(t, nil, fakeLM(0, 0, false, "Meta-0"), fakeLM(1920, 0, true, "eDP-1"))
	waitUntil(t, "the move to be seen", func() bool { return d.Target().Rect.X == 0 })
	// Stopping the screen share from GNOME's indicator removes the monitor.
	f.stopSharing(f.cast(0))
	waitForMutterRestart(t, d)
	d.Close()

	d = newTestMutterDisplay(t, 1920, 1080)
	if f.castCount() != 2 {
		t.Fatalf("%d monitors created, want 2", f.castCount())
	}
	if got, want := f.arrangement(), "Meta-0@0,0 1920x1080 eDP-1@1920,0 1920x1080*"; got != want {
		t.Errorf("arrangement = %q, want %q", got, want)
	}
	if got, want := d.Target().Rect, (displayRect{X: 0, Y: 0, W: 1920, H: 1080}); got != want {
		t.Errorf("Rect = %+v, want %+v", got, want)
	}
	if mapped := s.mappedTo(); mapped[len(mapped)-1].Serial != "0x000002" {
		t.Errorf("Drawsplay Pen mapped to %v, want the new monitor", mapped[len(mapped)-1])
	}
}

func TestMutterVirtualMonitorSavesTheResolutionPickedInSettings(t *testing.T) {
	store := withTestSettingsStore(t)
	f := newFakeLaptopMutter()
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	if n := f.applyCount(); n != 0 {
		t.Errorf("%d configurations applied for a monitor GNOME put in place, want 0", n)
	}
	f.configure(t, map[string][2]int{"Meta-0": {2560, 1440}}, fakeLM(0, 0, true, "eDP-1"), fakeLM(1920, 0, false, "Meta-0"))
	waitForMutterRestart(t, d)
	if got := store.get(); got.Width != 2560 || got.Height != 1440 {
		t.Errorf("saved resolution = %dx%d, want 2560x1440", got.Width, got.Height)
	}
	d.Close()

	// The restarted session finds the monitor as the user left it.
	d = newTestMutterDisplay(t, 2560, 1440)
	if f.castCount() != 1 || f.applyCount() != 0 {
		t.Errorf("%d monitors created and %d configurations applied, want 1 and 0", f.castCount(), f.applyCount())
	}
	if got := d.Target(); got.Width != 2560 || got.Height != 1440 {
		t.Errorf("Target = %dx%d, want 2560x1440", got.Width, got.Height)
	}

	// Picking the monitor's preferred mode is a choice, too.
	f.configure(t, map[string][2]int{"Meta-0": {1920, 1080}}, fakeLM(0, 0, true, "eDP-1"), fakeLM(1920, 0, false, "Meta-0"))
	waitForMutterRestart(t, d)
	if got := store.get(); got.Width != 1920 || got.Height != 1080 {
		t.Errorf("saved resolution = %dx%d, want 1920x1080", got.Width, got.Height)
	}
}

// GNOME puts every monitor in its preferred mode when one is plugged in;
// that is not the user's choice.
func TestMutterVirtualMonitorDoesNotSaveTheModeAfterAHotplug(t *testing.T) {
	store := withTestSettingsStore(t)
	f := newFakeLaptopMutter()
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	f.configure(t, map[string][2]int{"Meta-0": {2560, 1440}}, fakeLM(0, 0, true, "eDP-1"), fakeLM(1920, 0, false, "Meta-0"))
	waitForMutterRestart(t, d)
	d.Close()
	d = newTestMutterDisplay(t, 2560, 1440)

	f.plug(fakeExternalMonitor())
	waitForMutterRestart(t, d)
	if got := store.get(); got.Width != 2560 || got.Height != 1440 {
		t.Errorf("saved resolution = %dx%d, want 2560x1440", got.Width, got.Height)
	}
	d.Close()

	d = newTestMutterDisplay(t, 2560, 1440)
	if m, _ := f.monitor("Meta-0"); m.current != fakeModeID(2560, 1440) {
		t.Errorf("virtual monitor mode = %s, want 2560x1440", m.current)
	}
	if got := d.Target(); got.Width != 2560 || got.Height != 1440 || got.Rect.X != 4480 {
		t.Errorf("Target = %+v, want 2560x1440 right of the other displays", got)
	}
	if f.castCount() != 1 {
		t.Errorf("%d monitors created, want 1", f.castCount())
	}
}

func TestMutterVirtualMonitorFollowsAMove(t *testing.T) {
	f := newFakeLaptopMutter()
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	f.configure(t, nil, fakeLM(0, 0, true, "eDP-1"), fakeLM(0, 1080, false, "Meta-0"))
	want := displayRect{X: 0, Y: 1080, W: 1920, H: 1080}
	waitUntil(t, "the move to be seen", func() bool { return d.Target().Rect == want })
	if !mutterWatching(d) {
		t.Error("a move restarted the session")
	}
	if got, err := d.Refresh(); err != nil || got.Rect != want {
		t.Errorf("Refresh = %+v, %v; want the new place", got, err)
	}
	mutterExt.mu.Lock()
	seen := mutterExt.seen
	mutterExt.mu.Unlock()
	if seen == nil || *seen != (x11Placement{Side: "below"}) {
		t.Errorf("seen = %v, want below", seen)
	}
}

func TestMutterVirtualMonitorMirroring(t *testing.T) {
	f := newFakeLaptopMutter()
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	f.configure(t, nil, fakeLM(0, 0, true, "eDP-1", "Meta-0"))
	waitForMutterRestart(t, d)
	d.Close()

	d = newTestMutterDisplay(t, 1920, 1080)
	if !d.Target().Mirrored {
		t.Error("the session does not know the monitor mirrors the laptop panel")
	}
	if mode, _ := displayModeFromTarget(d.Target()); mode != "mirror" {
		t.Errorf("display mode = %s, want mirror", mode)
	}
	if n := f.applyCount(); n != 0 {
		t.Errorf("%d configurations applied, want 0 (the mirror is kept)", n)
	}
}

// Older GNOME Shell versions let the user switch the virtual monitor off.
func TestMutterVirtualMonitorSwitchedOff(t *testing.T) {
	f := newFakeLaptopMutter()
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	f.switchOff("Meta-0")
	waitForMutterRestart(t, d)
	d.Close()

	d = newTestMutterDisplay(t, 1920, 1080)
	if got, want := f.arrangement(), "Meta-0@1920,0 1920x1080 eDP-1@0,0 1920x1080*"; got != want {
		t.Errorf("arrangement = %q, want %q", got, want)
	}
	if got := d.Target().Rect; got != (displayRect{X: 1920, Y: 0, W: 1920, H: 1080}) {
		t.Errorf("Rect = %+v", got)
	}
}

func TestMutterVirtualMonitorGoesWhenTheHostExits(t *testing.T) {
	resetX11Ext()
	t.Cleanup(resetX11Ext)
	f := newFakeDeskMutter(t)
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)

	releaseVirtualDisplaysOnExit()
	if _, ok := f.monitor("Meta-0"); ok {
		t.Error("the virtual monitor is still there")
	}
	if got := f.arrangement(); got != fakeDeskArrangement {
		t.Errorf("arrangement = %q, want %q", got, fakeDeskArrangement)
	}
	d.Close()
	if mutterReleasePending() {
		t.Error("closing the session after exit started a release")
	}
	if _, err := newMutterDisplay(videoOptions{Width: 1920, Height: 1080}); err == nil {
		t.Error("a session started while the host exits")
	}
	if err := d.Recover(); err == nil {
		t.Error("Recover succeeded while the host exits")
	}
	if n := f.castCount(); n != 1 {
		t.Errorf("%d monitors created, want 1", n)
	}
}

func TestMutterVirtualMonitorRecover(t *testing.T) {
	f := newFakeDeskMutter(t)
	s := withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	if err := d.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if f.castCount() != 2 {
		t.Fatalf("%d monitors created, want 2", f.castCount())
	}
	f.mu.Lock()
	disconnected := f.casts[0].disconnected
	f.mu.Unlock()
	if !disconnected {
		t.Error("the old screen cast session is still open")
	}
	want := captureTarget{
		Kind: capturePipeWire, Node: 41, Width: 1920, Height: 1080,
		Rect: displayRect{X: 2560, Y: 0, W: 1920, H: 1080}, TabletMapped: true,
	}
	if got := d.Target(); got != want {
		t.Errorf("Target = %+v, want %+v", got, want)
	}
	if mapped := s.mappedTo(); len(mapped) != 2 || mapped[1] != f.cast(1).Monitor() {
		t.Errorf("Drawsplay Pen mapped to %v, want the new monitor", mapped)
	}
	if n := f.applyCount(); n != 2 {
		t.Errorf("%d configurations applied, want 2", n)
	}

	// The watcher follows the new monitor.
	f.configure(t, nil, fakeLM(0, 0, false, "HDMI-1"), fakeLM(320, 1440, true, "eDP-1"), fakeLM(320, 2520, false, "Meta-0"))
	waitUntil(t, "the move to be seen", func() bool { return d.Target().Rect.Y == 2520 })
	if !mutterWatching(d) {
		t.Error("the watcher stopped")
	}
}

func TestMutterVirtualMonitorReplacedForAResolutionItLacks(t *testing.T) {
	f := newFakeLaptopMutter()
	withFakeMutter(t, f)
	newTestMutterDisplay(t, 1920, 1080).Close()

	d := newTestMutterDisplay(t, 1366, 768)
	if f.castCount() != 2 {
		t.Errorf("%d monitors created, want 2", f.castCount())
	}
	if got := d.Target(); got.Width != 1366 || got.Height != 768 {
		t.Errorf("Target = %dx%d, want 1366x768", got.Width, got.Height)
	}
}

func TestMutterVirtualMonitorIgnoresAnOlderSession(t *testing.T) {
	f := newFakeLaptopMutter()
	withFakeMutter(t, f)
	d1 := newTestMutterDisplay(t, 1920, 1080)
	newTestMutterDisplay(t, 1920, 1080)
	d1.Close()
	mutterExt.mu.Lock()
	claimed := mutterExt.claimed
	mutterExt.mu.Unlock()
	if !claimed || mutterReleasePending() {
		t.Error("closing the older session released the virtual monitor")
	}
	if err := d1.Recover(); err == nil {
		t.Error("the older session replaced the virtual monitor")
	}
}

func TestMutterVirtualMonitorRefresh(t *testing.T) {
	f := newFakeLaptopMutter()
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)

	f.setFailState(errors.New("DisplayConfig is not responding"))
	if got, err := d.Refresh(); err != nil || got != d.Target() {
		t.Errorf("Refresh = %+v, %v; want the last target", got, err)
	}
	f.setFailState(nil)

	f.switchOff("Meta-0")
	if _, err := d.Refresh(); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("Refresh of a monitor switched off: err = %v", err)
	}
	f.stopSharing(f.cast(0))
	if _, err := d.Refresh(); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Errorf("Refresh of a removed monitor: err = %v", err)
	}
}

func TestMutterVirtualMonitorUnavailable(t *testing.T) {
	f := newFakeLaptopMutter()
	f.startErr = errors.New("screen casting is unavailable")
	withFakeMutter(t, f)
	if _, err := newMutterDisplay(videoOptions{Width: 1920, Height: 1080}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("newMutterDisplay: err = %v", err)
	}
	mutterExt.mu.Lock()
	claimed, held := mutterExt.claimed, mutterExt.held
	mutterExt.mu.Unlock()
	if claimed || held != nil || mutterReleasePending() {
		t.Error("a failed session left the virtual monitor claimed")
	}
}

// --- GNOME 49 and older ------------------------------------------------------------

// newFakeLegacyDeskMutter is newFakeDeskMutter on GNOME 49 or older.
func newFakeLegacyDeskMutter(t *testing.T) *fakeMutter {
	f := newFakeDeskMutter(t)
	f.legacy = true
	return f
}

func TestMutterLegacyVirtualMonitorIsAskedForBySize(t *testing.T) {
	f := newFakeLegacyDeskMutter(t)
	s := withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)

	if n := f.primeCount(); n != 1 {
		t.Errorf("the stream was asked for a size %d times, want once", n)
	}
	if got, want := f.arrangement(), "HDMI-1@0,0 2560x1440 Meta-0@2560,0 1920x1080 eDP-1@320,1440 1920x1080*"; got != want {
		t.Errorf("arrangement = %q, want %q", got, want)
	}
	want := captureTarget{
		Kind: capturePipeWire, Node: 40, Width: 1920, Height: 1080,
		Rect: displayRect{X: 2560, Y: 0, W: 1920, H: 1080}, TabletMapped: true,
		SizeFromConsumer: true, HostResolution: true,
	}
	if got := d.Target(); got != want {
		t.Errorf("Target = %+v, want %+v", got, want)
	}
	if got := s.mappedTo(); !reflect.DeepEqual(got, []monitorSpec{f.cast(0).Monitor()}) {
		t.Errorf("Drawsplay Pen mapped to %v, want the virtual monitor", got)
	}
	if got := resolutionControlOf(d.Target()); got != "host" {
		t.Errorf("resolutionControl = %q, want host: Settings offers no other resolution", got)
	}
}

func TestMutterVirtualMonitorIsNotAskedForBySizeOnGNOME50(t *testing.T) {
	f := newFakeDeskMutter(t)
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1920, 1080)
	if n := f.primeCount(); n != 0 {
		t.Errorf("the stream was asked for a size %d times, want never", n)
	}
	if got := d.Target(); got.SizeFromConsumer || got.HostResolution {
		t.Errorf("Target = %+v; GNOME 50 sizes the monitor from its modes", got)
	}
}

func TestMutterLegacyVirtualMonitorHasAnEvenSize(t *testing.T) {
	f := newFakeLaptopMutter()
	f.legacy = true
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1365, 767)
	want := displayRect{X: 1920, Y: 0, W: 1364, H: 766}
	if got := d.Target(); got.Width != 1364 || got.Height != 766 || got.Rect != want {
		t.Errorf("Target = %+v, want a 1364x766 monitor at %+v", got, want)
	}
	d.Close()

	d = newTestMutterDisplay(t, 1365, 767)
	if f.castCount() != 1 || f.primeCount() != 1 {
		t.Errorf("%d monitors created and %d sizes asked for, want the one monitor kept", f.castCount(), f.primeCount())
	}
	if got := d.Target(); got.Rect != want {
		t.Errorf("Target = %+v, want the monitor at %+v", got, want)
	}
}

func TestMutterLegacyVirtualMonitorIsResizedWhereItIs(t *testing.T) {
	f := newFakeLegacyDeskMutter(t)
	s := withFakeMutter(t, f)
	newTestMutterDisplay(t, 1920, 1080).Close()
	f.configure(t, nil, fakeLM(0, 0, false, "HDMI-1"), fakeLM(320, 1440, true, "eDP-1"), fakeLM(320, 2520, false, "Meta-0"))

	d := newTestMutterDisplay(t, 1280, 720)
	if f.castCount() != 1 || f.primeCount() != 2 {
		t.Errorf("%d monitors created and %d sizes asked for, want the one monitor resized", f.castCount(), f.primeCount())
	}
	if got, want := f.arrangement(), "HDMI-1@0,0 2560x1440 Meta-0@320,2520 1280x720 eDP-1@320,1440 1920x1080*"; got != want {
		t.Errorf("arrangement = %q, want %q", got, want)
	}
	if got, want := d.Target().Rect, (displayRect{X: 320, Y: 2520, W: 1280, H: 720}); got != want {
		t.Errorf("Target.Rect = %+v, want %+v", got, want)
	}
	if got := d.Target(); got.Width != 1280 || got.Height != 720 || !got.SizeFromConsumer {
		t.Errorf("Target = %+v, want a 1280x720 consumer-sized stream", got)
	}
	if n := len(s.mappedTo()); n != 2 {
		t.Errorf("Drawsplay Pen mapped %d times, want once per session", n)
	}
}

func TestMutterLegacyVirtualMonitorReplacedWhenItCannotBeResized(t *testing.T) {
	f := newFakeLegacyDeskMutter(t)
	withFakeMutter(t, f)
	newTestMutterDisplay(t, 1920, 1080).Close()
	f.mu.Lock()
	f.legacy = false // the stream no longer follows its consumer
	f.mu.Unlock()

	d := newTestMutterDisplay(t, 1280, 720)
	if f.castCount() != 2 {
		t.Errorf("%d monitors created, want a new one", f.castCount())
	}
	if got := d.Target(); got.Width != 1280 || got.Height != 720 {
		t.Errorf("Target = %dx%d, want 1280x720", got.Width, got.Height)
	}
}

func TestMutterLegacyVirtualMonitorUnavailableWithoutAConsumer(t *testing.T) {
	f := newFakeLegacyDeskMutter(t)
	f.primeErr = errors.New(`no element "pipewiresrc"`)
	withFakeMutter(t, f)
	_, err := newMutterDisplay(videoOptions{Width: 1920, Height: 1080})
	if !errors.Is(err, errMutterMonitorNotListed) || !strings.Contains(err.Error(), "pipewiresrc") {
		t.Errorf("newMutterDisplay: err = %v", err)
	}
	if f.castCount() != 1 || !f.ended(f.cast(0)) {
		t.Error("the screen cast session was left running")
	}
	if heldMutterCast() != nil {
		t.Error("a failed session left a virtual monitor held")
	}
}

func TestMutterLegacyVirtualMonitorKeepsTheConfiguredResolution(t *testing.T) {
	f := newFakeLegacyDeskMutter(t)
	withFakeMutter(t, f)
	d := newTestMutterDisplay(t, 1365, 767)
	// A move makes the watcher look at the 1364x766 monitor.
	f.configure(t, nil, fakeLM(0, 0, false, "HDMI-1"), fakeLM(320, 1440, true, "eDP-1"), fakeLM(320, 2520, false, "Meta-0"))
	waitUntil(t, "the move to be seen", func() bool { return d.Target().Rect.Y == 2520 })
	d.mu.Lock()
	reqW, reqH := d.reqW, d.reqH
	d.mu.Unlock()
	if reqW != 1365 || reqH != 767 {
		t.Errorf("the session now asks for %dx%d, want the configured 1365x767", reqW, reqH)
	}
}
