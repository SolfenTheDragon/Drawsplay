//go:build linux

package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeWlExt is a compositor behind a fake extend backend.
type fakeWlExt struct {
	mu        sync.Mutex
	arranges  bool
	anchored  bool
	outputs   []wlExtOutput
	reloads   uint64
	created   int
	removed   []string
	mapped    int
	transform int // the transform the compositor gives new outputs (KWin's memory)
	changed   chan struct{}
	handles   map[string]*fakeWlExtHandle
}

func newFakeWlExtComp(arranges, anchored bool, outputs ...wlExtOutput) *fakeWlExt {
	return &fakeWlExt{arranges: arranges, anchored: anchored, outputs: outputs, changed: make(chan struct{}, 1), handles: map[string]*fakeWlExtHandle{}}
}

func (c *fakeWlExt) Label() string  { return "Fake" }
func (c *fakeWlExt) Arranges() bool { return c.arranges }
func (c *fakeWlExt) Anchored() bool { return c.anchored }
func (c *fakeWlExt) LogicalSize(w, h int, scale float64) (int, int) {
	return int(float64(w) / scale), int(float64(h) / scale)
}

func (c *fakeWlExt) Create(modeW, modeH int) (wlExtHandle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created++
	o := wlExtOutput{Name: fmt.Sprintf("HEADLESS-%d", c.created), Active: true, ModeW: modeW, ModeH: modeH, Scale: 1, Virtual: true, Transform: c.transform}
	var rects []displayRect
	for _, other := range c.outputs {
		if other.Active {
			rects = append(rects, other.Rect)
		}
	}
	b := boundsOf(rects)
	uw, uh := o.upright()
	o.Rect = displayRect{X: b.X + b.W, Y: b.Y, W: uw, H: uh}
	c.outputs = append(c.outputs, o)
	h := &fakeWlExtHandle{c: c, name: o.Name, closed: make(chan struct{})}
	c.handles[o.Name] = h
	return h, nil
}

// set changes an output as the user would and notifies the watcher.
func (c *fakeWlExt) set(name string, f func(o *wlExtOutput)) {
	c.mu.Lock()
	for i := range c.outputs {
		if c.outputs[i].Name == name {
			f(&c.outputs[i])
			uw, uh := c.outputs[i].upright()
			c.outputs[i].Rect.W, c.outputs[i].Rect.H = c.LogicalSize(uw, uh, c.outputs[i].Scale)
		}
	}
	c.mu.Unlock()
	c.notify()
}

func (c *fakeWlExt) notify() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *fakeWlExt) counts() (created, mapped int, removed []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.created, c.mapped, append([]string(nil), c.removed...)
}

func (c *fakeWlExt) output(name string) (wlExtOutput, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return wlExtState{Outputs: c.outputs}.output(name)
}

type fakeWlExtHandle struct {
	c      *fakeWlExt
	name   string
	closed chan struct{}
}

func (h *fakeWlExtHandle) Name() string             { return h.name }
func (h *fakeWlExtHandle) Changed() <-chan struct{} { return h.c.changed }
func (h *fakeWlExtHandle) Closed() <-chan struct{}  { return h.closed }
func (h *fakeWlExtHandle) Target() captureTarget {
	return captureTarget{Kind: captureScreencopy, OutputName: h.name}
}

func (h *fakeWlExtHandle) State() (wlExtState, error) {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return wlExtState{Outputs: append([]wlExtOutput(nil), h.c.outputs...), Reloads: h.c.reloads}, nil
}

func (h *fakeWlExtHandle) MapTablet() error {
	h.c.mu.Lock()
	h.c.mapped++
	h.c.mu.Unlock()
	return nil
}

func (h *fakeWlExtHandle) Configure(cfg wlExtConfig) error {
	c := h.c
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.outputs {
		o := &c.outputs[i]
		if o.Name == h.name {
			o.Active, o.ModeW, o.ModeH, o.Transform, o.Scale = true, cfg.ModeW, cfg.ModeH, cfg.Transform, cfg.Scale
			uw, uh := o.upright()
			lw, lh := c.LogicalSize(uw, uh, cfg.Scale)
			o.Rect = displayRect{X: cfg.X, Y: cfg.Y, W: lw, H: lh}
		}
		for _, m := range cfg.Moves {
			if o.Name == m.Name {
				o.Rect.X, o.Rect.Y = m.X, m.Y
			}
		}
	}
	return nil
}

func (h *fakeWlExtHandle) Remove(moves []x11OutputPos, fallback string) error {
	c := h.c
	c.mu.Lock()
	defer c.mu.Unlock()
	var keep []wlExtOutput
	for _, o := range c.outputs {
		if o.Name == h.name {
			continue
		}
		for _, m := range moves {
			if o.Name == m.Name {
				o.Rect.X, o.Rect.Y = m.X, m.Y
			}
		}
		if o.Name == fallback {
			o.Active = true
		}
		keep = append(keep, o)
	}
	c.outputs = keep
	c.removed = append(c.removed, h.name)
	return nil
}

func withFakeWlExt(t *testing.T, c *fakeWlExt) {
	t.Helper()
	prevGrace, prevSettle, prevPoll := wlExtReleaseGrace, wlExtSettle, wlExtPoll
	wlExtReleaseGrace, wlExtSettle, wlExtPoll = time.Hour, time.Millisecond, 20*time.Millisecond
	resetWlExt()
	t.Cleanup(func() {
		resetWlExt()
		wlExtReleaseGrace, wlExtSettle, wlExtPoll = prevGrace, prevSettle, prevPoll
	})
}

func resetWlExt() {
	wlExt.mu.Lock()
	defer wlExt.mu.Unlock()
	stopWlExtReleaseTimerLocked()
	wlExt.gen, wlExt.claimed, wlExt.exiting = 0, false, false
	wlExt.held, wlExt.backend = nil, nil
	wlExt.seen, wlExt.placement, wlExt.look = nil, nil, nil
}

func newTestWlExtDisplay(t *testing.T, c *fakeWlExt, w, h int) *wlExtDisplay {
	t.Helper()
	d, err := newWlExtDisplay(c, videoOptions{Width: w, Height: h})
	if err != nil {
		t.Fatalf("newWlExtDisplay: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func waitForWlExtRestart(t *testing.T, d *wlExtDisplay) {
	t.Helper()
	select {
	case <-d.watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not restart the session")
	}
}

func wlExtWatching(d *wlExtDisplay) bool {
	select {
	case <-d.watchDone:
		return false
	default:
		return true
	}
}

var fakeLaptop = wlExtOutput{Name: "eDP-1", Active: true, Rect: displayRect{W: 1920, H: 1080}, ModeW: 1920, ModeH: 1080, Scale: 1}

func TestWlExtCreatesOutputRightOfTheDesktop(t *testing.T) {
	c := newFakeWlExtComp(true, true, fakeLaptop)
	withFakeWlExt(t, c)
	d := newTestWlExtDisplay(t, c, 2960, 1848)
	got := d.Target()
	want := captureTarget{Kind: captureScreencopy, OutputName: "HEADLESS-1", Width: 2960, Height: 1848,
		Rect: displayRect{X: 1920, Y: 0, W: 2960, H: 1848}, TabletMapped: true}
	if got != want {
		t.Fatalf("target = %+v, want %+v", got, want)
	}
	if _, mapped, _ := c.counts(); mapped != 1 {
		t.Fatalf("pen mapped %d times, want 1", mapped)
	}
}

func TestWlExtKeepsOutputAcrossReconnectAndRemovesItAfterGrace(t *testing.T) {
	c := newFakeWlExtComp(true, true, fakeLaptop)
	withFakeWlExt(t, c)
	d := newTestWlExtDisplay(t, c, 2960, 1848)
	// The user puts it left of the laptop panel: the desktop's origin
	// stays at 0,0.
	c.mu.Lock()
	c.outputs[0].Rect.X = 2960
	c.outputs[1].Rect.X = 0
	c.mu.Unlock()
	c.notify()
	waitUntil(t, "the move", func() bool { return d.Target().Rect.X == 0 })
	d.Close()
	d2 := newTestWlExtDisplay(t, c, 2960, 1848)
	if created, _, removed := c.counts(); created != 1 || len(removed) != 0 {
		t.Fatalf("reconnect made %d outputs and removed %v, want the same output", created, removed)
	}
	d2.Close()
	wlExt.mu.Lock()
	stopHeldWlExtLocked()
	wlExt.mu.Unlock()
	if laptop, _ := c.output("eDP-1"); laptop.Rect.X != 0 {
		t.Fatalf("laptop panel left at %+v, want back at the origin", laptop.Rect)
	}
	// The next output goes back on the left.
	d3 := newTestWlExtDisplay(t, c, 2960, 1848)
	if r := d3.Target().Rect; r != (displayRect{X: 0, Y: 0, W: 2960, H: 1848}) {
		t.Fatalf("new output at %+v, want left of the laptop panel", r)
	}
	if laptop, _ := c.output("eDP-1"); laptop.Rect.X != 2960 {
		t.Fatalf("laptop panel at %+v, want moved right", laptop.Rect)
	}
}

func TestWlExtSavesUserResolutionAndRotation(t *testing.T) {
	store := withTestSettingsStore(t)
	c := newFakeWlExtComp(true, true, fakeLaptop)
	withFakeWlExt(t, c)
	d := newTestWlExtDisplay(t, c, 2960, 1848)
	c.set("HEADLESS-1", func(o *wlExtOutput) { o.Transform = wlOutputTransform90 })
	waitForWlExtRestart(t, d)
	if s := store.get(); s.Width != 1848 || s.Height != 2960 {
		t.Fatalf("saved %dx%d, want the rotated 1848x2960", s.Width, s.Height)
	}
	d.Close()
	// The next session keeps the turned output.
	d2 := newTestWlExtDisplay(t, c, 1848, 2960)
	if created, _, _ := c.counts(); created != 1 {
		t.Fatalf("made %d outputs, want the turned one kept", created)
	}
	if got := d2.Target(); got.Width != 1848 || got.Height != 2960 {
		t.Fatalf("target %dx%d, want 1848x2960", got.Width, got.Height)
	}
}

func TestWlExtDoesNotSaveReloadOrHotplugSizes(t *testing.T) {
	store := withTestSettingsStore(t)
	before := store.get()
	c := newFakeWlExtComp(true, true, fakeLaptop)
	withFakeWlExt(t, c)
	d := newTestWlExtDisplay(t, c, 2960, 1848)
	_, mapped, _ := c.counts()
	c.mu.Lock()
	c.reloads++
	c.mu.Unlock()
	c.set("HEADLESS-1", func(o *wlExtOutput) { o.ModeW, o.ModeH = 1920, 1080 })
	waitForWlExtRestart(t, d)
	if s := store.get(); s.Width != before.Width || s.Height != before.Height {
		t.Fatalf("a reload's size was saved: %dx%d", s.Width, s.Height)
	}
	if _, now, _ := c.counts(); now != mapped+1 {
		t.Fatalf("pen not mapped again after the reload")
	}
}

func TestWlExtMoveOnlyUpdatesPlace(t *testing.T) {
	c := newFakeWlExtComp(true, true, fakeLaptop)
	withFakeWlExt(t, c)
	d := newTestWlExtDisplay(t, c, 2960, 1848)
	c.set("HEADLESS-1", func(o *wlExtOutput) { o.Rect.Y = 200 })
	waitUntil(t, "the move", func() bool { return d.Target().Rect.Y == 200 })
	// A scale change only changes the output's place in the layout too.
	c.set("HEADLESS-1", func(o *wlExtOutput) { o.Scale = 2 })
	waitUntil(t, "the rescale", func() bool { return d.Target().Rect.W == 1480 })
	time.Sleep(50 * time.Millisecond)
	if !wlExtWatching(d) {
		t.Fatal("a move restarted the session")
	}
}

func TestWlExtRestartsWhenSwitchedOffOrMirrored(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    func(o *wlExtOutput)
	}{
		{"off", func(o *wlExtOutput) { o.Active = false }},
		{"overlap", func(o *wlExtOutput) { o.Rect.X = 0 }},
		{"mirror", func(o *wlExtOutput) { o.MirrorOf = "eDP-1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeWlExtComp(true, true, fakeLaptop)
			withFakeWlExt(t, c)
			d := newTestWlExtDisplay(t, c, 2960, 1848)
			c.set("HEADLESS-1", tc.f)
			waitForWlExtRestart(t, d)
		})
	}
}

func TestWlExtSwitchesFallbackOnWhenOnlyDisplay(t *testing.T) {
	c := newFakeWlExtComp(true, true, fakeLaptop)
	withFakeWlExt(t, c)
	newTestWlExtDisplay(t, c, 2960, 1848)
	c.mu.Lock()
	c.outputs[0].Active = false
	c.mu.Unlock()
	releaseWlExtOnExit()
	if laptop, _ := c.output("eDP-1"); !laptop.Active {
		t.Fatal("the laptop panel stayed off with the extended display gone")
	}
	if _, err := newWlExtDisplay(c, videoOptions{Width: 1, Height: 1}); err == nil {
		t.Fatal("a session started while the host exits")
	}
}

func TestWlExtRecoverReplacesOutput(t *testing.T) {
	c := newFakeWlExtComp(true, true, fakeLaptop)
	withFakeWlExt(t, c)
	d := newTestWlExtDisplay(t, c, 2960, 1848)
	if err := d.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := d.Target().OutputName; got != "HEADLESS-2" {
		t.Fatalf("recovered onto %s, want a new output", got)
	}
	time.Sleep(50 * time.Millisecond)
	if !wlExtWatching(d) {
		t.Fatal("Recover restarted the session")
	}
	c.set("HEADLESS-2", func(o *wlExtOutput) { o.Active = false })
	waitForWlExtRestart(t, d)
}

func TestWlExtClosedOutputRestartsSession(t *testing.T) {
	c := newFakeWlExtComp(false, false, fakeLaptop)
	withFakeWlExt(t, c)
	d := newTestWlExtDisplay(t, c, 2960, 1848)
	close(c.handles["HEADLESS-1"].closed)
	waitForWlExtRestart(t, d)
}

// KWin turns a new output as it remembers the last one: the host makes one
// whose mode turns into the configured size.
func TestWlExtRecreatesOutputTurnedByCompositor(t *testing.T) {
	c := newFakeWlExtComp(false, false, fakeLaptop)
	c.transform = wlOutputTransform90
	withFakeWlExt(t, c)
	d := newTestWlExtDisplay(t, c, 2960, 1848)
	if got := d.Target(); got.Width != 2960 || got.Height != 1848 || got.OutputName != "HEADLESS-2" {
		t.Fatalf("target %+v, want a second, turned 2960x1848 output", got)
	}
	if _, _, removed := c.counts(); len(removed) != 1 || removed[0] != "HEADLESS-1" {
		t.Fatalf("removed %v, want the first output", removed)
	}
}

func TestPlanExtendLayoutUnanchoredLeavesOthers(t *testing.T) {
	outs := []extendOutput{{Name: "DP-1", Active: true, Rect: displayRect{W: 1920, H: 1080}}}
	l := planExtendLayout(outs, "Drawsplay", 1280, 800, &x11Placement{Side: "left"}, false)
	if l.Rect != (displayRect{X: -1280, Y: 0, W: 1280, H: 800}) || len(l.Moves) != 0 {
		t.Fatalf("layout = %+v", l)
	}
	l = planExtendLayout(nil, "Drawsplay", 1280, 800, nil, true)
	if l.Rect != (displayRect{W: 1280, H: 800}) {
		t.Fatalf("lone layout = %+v", l)
	}
}
