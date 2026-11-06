//go:build linux

package main

// Extended display on Wayland compositors other than GNOME: KDE Plasma
// (KWin, kwin_extend_linux.go), sway (sway_extend_linux.go) and Hyprland
// (hypr_extend_linux.go). Each backend creates a virtual output through the
// compositor's own interface, maps the Drawsplay Pen onto it and names the
// stream to capture; this file gives them the X11 extended display's
// behaviour (x11_extend_linux.go):
//
//   - the output appears next to the other displays at the configured
//     resolution, on the side the user last had it, and survives a quick
//     reconnect;
//   - a resolution or orientation the user picks in the desktop's display
//     settings is saved as the configured resolution and restarts the
//     session, as do switching the output off and mirroring;
//   - a move only updates the output's place: capture and the pen follow
//     the output anyway;
//   - the output goes a few seconds after the last session ends, or when
//     the host exits, and the other displays are put back.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// Test hooks.
var (
	wlExtReleaseGrace = 5 * time.Second
	wlExtSettle       = 400 * time.Millisecond
	// wlExtPoll backs up compositors that do not report every change.
	wlExtPoll = time.Second
)

// wlExtOutput is one of the compositor's outputs.
type wlExtOutput struct {
	Name   string
	Active bool
	// Rect is the output's place in the layout, in logical (scaled)
	// coordinates.
	Rect         displayRect
	ModeW, ModeH int // current mode, in pixels, before the transform
	Transform    int // wl_output transform
	Scale        float64
	// MirrorOf names the output this one mirrors (Hyprland).
	MirrorOf string
	// Virtual: a headless output, which the host never falls back to.
	Virtual bool
}

// upright is the output's size in pixels as it is shown.
func (o wlExtOutput) upright() (int, int) {
	if wlTransformSwapsAxes(o.Transform) {
		return o.ModeH, o.ModeW
	}
	return o.ModeW, o.ModeH
}

type wlExtState struct {
	Outputs []wlExtOutput
	// Reloads counts configuration reloads, which reset the outputs'
	// runtime configuration.
	Reloads uint64
}

func (st wlExtState) output(name string) (wlExtOutput, bool) {
	for _, o := range st.Outputs {
		if o.Name == name {
			return o, true
		}
	}
	return wlExtOutput{}, false
}

// wlExtConfig is how the host sets the virtual output up.
type wlExtConfig struct {
	ModeW, ModeH int
	X, Y         int
	Transform    int
	Scale        float64
	// Moves keep the desktop's origin at 0,0 (see wlExtBackend.Anchored).
	Moves []outputPos
}

// wlExtBackend is a compositor's way of making virtual outputs.
type wlExtBackend interface {
	Label() string
	// Create adds a virtual output showing a modeW x modeH mode.
	Create(modeW, modeH int) (wlExtHandle, error)
	// Arranges: the host sets the output's mode and place. Otherwise the
	// compositor lays the output out from its own remembered configuration,
	// and the output's mode is fixed at creation (KWin).
	Arranges() bool
	// Anchored: the host keeps the desktop's origin at 0,0, as on X11.
	Anchored() bool
	// LogicalSize is the layout size of a w x h output at scale.
	LogicalSize(w, h int, scale float64) (int, int)
}

// wlExtHandle is one virtual output.
type wlExtHandle interface {
	Name() string
	State() (wlExtState, error)
	// Changed hears of changes to the outputs; nil when only polling sees
	// them.
	Changed() <-chan struct{}
	// Closed is closed when the compositor removed the output by itself.
	Closed() <-chan struct{}
	Configure(cfg wlExtConfig) error
	// Target names what to capture; the manager fills in the size and
	// place.
	Target() captureTarget
	// MapTablet maps the Drawsplay Pen onto the output.
	MapTablet() error
	// Remove removes the output, moves the other displays, and switches the
	// fallback display on when it is not empty.
	Remove(moves []outputPos, fallback string) error
}

var errWlExtSize = errors.New("the output does not show the configured resolution")

// wlExtLook is how the user had the last virtual output turned and scaled.
type wlExtLook struct {
	Transform int
	Scale     float64
}

// wlExt holds the virtual output between sessions. mu also serializes every
// layout change the host makes.
var wlExt struct {
	heldOutput
	held    wlExtHandle
	backend wlExtBackend
	// seen is where the user last had the held output; placement and look
	// are where and how the last one was when it went (process lifetime).
	seen, placement *displayPlacement
	look            *wlExtLook
}

func chanClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// releaseWlExtLocked ends session gen's claim and removes the output after
// wlExtReleaseGrace unless another session claims it first.
func releaseWlExtLocked(gen uint64) {
	if !wlExt.unclaimLocked(gen) {
		return
	}
	wlExt.stopTimerLocked()
	if wlExt.exiting || wlExt.held == nil {
		return
	}
	wlExt.afterLocked(gen, wlExtReleaseGrace, stopHeldWlExtLocked)
}

// releaseWlExtOnExit removes the virtual output, which would otherwise stay
// on as an empty display (sway, Hyprland) after the host exits.
func releaseWlExtOnExit() {
	wlExt.mu.Lock()
	defer wlExt.mu.Unlock()
	wlExt.exitLocked()
	stopHeldWlExtLocked()
}

// planExtendRelease plans putting the other displays back when the output
// goes: back to the origin when anchored, or the fallback display switched
// on when no other display is on.
func planExtendRelease(st wlExtState, name string, anchored bool) (moves []outputPos, fallback string) {
	var rects []displayRect
	var others []wlExtOutput
	for _, o := range st.Outputs {
		if o.Name != name && o.Active {
			others = append(others, o)
			rects = append(rects, o.Rect)
		}
	}
	if len(others) == 0 {
		for _, o := range st.Outputs {
			if o.Name != name && !o.Virtual {
				return nil, o.Name
			}
		}
		return nil, ""
	}
	if b := boundsOf(rects); anchored && (b.X != 0 || b.Y != 0) {
		for _, o := range others {
			moves = append(moves, outputPos{Name: o.Name, X: o.Rect.X - b.X, Y: o.Rect.Y - b.Y})
		}
	}
	return moves, ""
}

// stopHeldWlExtLocked removes the held output, remembering where and how it
// was.
func stopHeldWlExtLocked() {
	hd, b := wlExt.held, wlExt.backend
	if hd == nil {
		return
	}
	wlExt.held = nil
	var moves []outputPos
	fallback := ""
	if !chanClosed(hd.Closed()) {
		if st, err := hd.State(); err == nil {
			obs := observeWlExt(st, hd.Name())
			if obs.Placed {
				p := obs.Placement
				wlExt.seen = &p
			}
			if obs.Active {
				wlExt.look = &wlExtLook{Transform: obs.Transform, Scale: obs.Scale}
			}
			if b.Arranges() {
				moves, fallback = planExtendRelease(st, hd.Name(), b.Anchored())
			}
		}
	}
	if wlExt.seen != nil {
		wlExt.placement = wlExt.seen
	}
	wlExt.seen = nil
	if err := hd.Remove(moves, fallback); err != nil {
		log.Printf("%s extended display %s: could not remove it: %v", b.Label(), hd.Name(), err)
		return
	}
	releaseDisplayClaim(displayClaimOutput, hd.Name())
	log.Printf("%s extended display %s removed", b.Label(), hd.Name())
}

// arrangeWlExtLocked makes the output show w x h (upright) next to the
// other displays. fresh: the output was just created, so it gets the
// orientation and scale the last one had rather than the compositor's.
func arrangeWlExtLocked(b wlExtBackend, hd wlExtHandle, w, h int, fresh bool) error {
	st, err := hd.State()
	if err != nil {
		return err
	}
	cur, ok := st.output(hd.Name())
	if !ok {
		return fmt.Errorf("%s is gone", hd.Name())
	}
	// A new output goes where the last one was, not where the compositor
	// put it.
	if uw, uh := cur.upright(); cur.Active && uw == w && uh == h && !(fresh && b.Arranges()) {
		return nil
	}
	if !b.Arranges() {
		return errWlExtSize
	}
	transform, scale := cur.Transform, cur.Scale
	if (fresh || !cur.Active) && wlExt.look != nil {
		transform, scale = wlExt.look.Transform, wlExt.look.Scale
	}
	if scale <= 0 {
		scale = 1
	}
	modeW, modeH := w, h
	if wlTransformSwapsAxes(transform) {
		modeW, modeH = h, w
	}
	lw, lh := b.LogicalSize(w, h, scale)
	var outputs []extendOutput
	for _, o := range st.Outputs {
		active := o.Active && o.MirrorOf == ""
		if o.Name == hd.Name() && fresh {
			// Plan it as switched off, so it goes where the last one was.
			active = false
		}
		outputs = append(outputs, extendOutput{Name: o.Name, Active: active, Rect: o.Rect})
	}
	layout := planExtendLayout(outputs, hd.Name(), lw, lh, wlExt.placement, b.Anchored())
	cfg := wlExtConfig{ModeW: modeW, ModeH: modeH, X: layout.Rect.X, Y: layout.Rect.Y, Transform: transform, Scale: scale, Moves: layout.Moves}
	if err := hd.Configure(cfg); err != nil {
		return err
	}
	if st, err = hd.State(); err != nil {
		return err
	}
	o, _ := st.output(hd.Name())
	if uw, uh := o.upright(); !o.Active || uw != w || uh != h {
		return fmt.Errorf("%s did not take the %dx%d mode", hd.Name(), w, h)
	}
	log.Printf("%s extended display %s: set to %dx%d at +%d+%d", b.Label(), hd.Name(), w, h, layout.Rect.X, layout.Rect.Y)
	return nil
}

// ensureWlExtLocked makes the held output show w x h next to the other
// displays, creating one when none is held or the held one cannot show
// w x h.
func ensureWlExtLocked(b wlExtBackend, w, h int) error {
	if hd := wlExt.held; hd != nil && (wlExt.backend != b || chanClosed(hd.Closed())) {
		stopHeldWlExtLocked()
	}
	if hd := wlExt.held; hd != nil {
		err := arrangeWlExtLocked(b, hd, w, h, false)
		if err == nil {
			return nil
		}
		log.Printf("%s extended display %s: %v; replacing it", b.Label(), hd.Name(), err)
		stopHeldWlExtLocked()
	}
	for attempt := 0; ; attempt++ {
		modeW, modeH := w, h
		if wlExt.look != nil && wlTransformSwapsAxes(wlExt.look.Transform) {
			modeW, modeH = h, w
		}
		hd, err := b.Create(modeW, modeH)
		if err != nil {
			return err
		}
		wlExt.held, wlExt.backend, wlExt.seen = hd, b, nil
		claimDisplay(displayClaimOutput, hd.Name())
		log.Printf("%s extended display %s created", b.Label(), hd.Name())
		err = arrangeWlExtLocked(b, hd, w, h, true)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errWlExtSize) || attempt > 0 {
			log.Printf("%s extended display %s: could not arrange the displays (%v); keeping the compositor's layout", b.Label(), hd.Name(), err)
			return nil
		}
		// The compositor turned the output as it remembers it: make one
		// whose mode turns into w x h.
		st, err := hd.State()
		if err != nil {
			return nil
		}
		o, _ := st.output(hd.Name())
		if !o.Active {
			return nil
		}
		wlExt.look = &wlExtLook{Transform: o.Transform, Scale: o.Scale}
		if uw, uh := o.upright(); uw != h || uh != w {
			return nil
		}
		stopHeldWlExtLocked()
	}
}

// wlExtObserved is the output's state as the watcher saw it.
type wlExtObserved struct {
	Present, Active bool
	Rect            displayRect
	W, H            int // upright size in pixels
	Transform       int
	Scale           float64
	Mirrored        bool
	Placement       displayPlacement
	Placed          bool
	// Outputs names the other outputs; a change is a hotplug.
	Outputs string
	Reloads uint64
}

func observeWlExt(st wlExtState, name string) wlExtObserved {
	obs := wlExtObserved{Reloads: st.Reloads}
	var names []string
	var rects []displayRect
	var cur wlExtOutput
	for _, o := range st.Outputs {
		if o.Name == name {
			cur, obs.Present = o, true
			continue
		}
		names = append(names, o.Name)
		if o.Active {
			rects = append(rects, o.Rect)
		}
	}
	obs.Outputs = strings.Join(names, ";")
	if !obs.Present || !cur.Active {
		return obs
	}
	obs.Active, obs.Rect, obs.Transform, obs.Scale = true, cur.Rect, cur.Transform, cur.Scale
	obs.W, obs.H = cur.upright()
	obs.Mirrored = cur.MirrorOf != ""
	for _, r := range rects {
		if rectsOverlap(r, cur.Rect) {
			obs.Mirrored = true
		}
	}
	if len(rects) > 0 && !obs.Mirrored {
		obs.Placement, obs.Placed = placementOf(cur.Rect, boundsOf(rects))
	}
	return obs
}

func wlExtTarget(hd wlExtHandle, obs wlExtObserved, label string, mapped bool) (captureTarget, error) {
	if !obs.Present {
		return captureTarget{}, fmt.Errorf("%s extended display %s is gone", label, hd.Name())
	}
	if !obs.Active {
		return captureTarget{}, fmt.Errorf("%s extended display %s is switched off", label, hd.Name())
	}
	t := hd.Target()
	// Encoders need even dimensions.
	t.Width, t.Height = obs.W&^1, obs.H&^1
	t.Rect, t.Mirrored, t.TabletMapped = obs.Rect, obs.Mirrored, mapped
	return t, nil
}

func mapWlExtTablet(label string, hd wlExtHandle) bool {
	if err := hd.MapTablet(); err != nil {
		log.Printf("%s extended display: could not map Drawsplay Pen input to %s (%v); touch/pen input may land on another display", label, hd.Name(), err)
		return false
	}
	log.Printf("Mapped Drawsplay Pen touch/pen input to %s (%s)", hd.Name(), label)
	return true
}

// wlExtDisplay is one session's use of the virtual output.
type wlExtDisplay struct {
	extendSession
	backend wlExtBackend
	held    wlExtHandle // guarded by mu
}

// newWlExtDisplay creates (or keeps, after a quick reconnect) the virtual
// output for a new session at the configured resolution.
func newWlExtDisplay(b wlExtBackend, opts videoOptions) (*wlExtDisplay, error) {
	w, h := opts.Width, opts.Height
	if w <= 0 || h <= 0 {
		w, h = x11DefaultWidth, x11DefaultHeight
	}
	wlExt.mu.Lock()
	defer wlExt.mu.Unlock()
	gen, err := wlExt.claimLocked()
	if err != nil {
		return nil, err
	}
	if err := ensureWlExtLocked(b, w, h); err != nil {
		releaseWlExtLocked(gen)
		return nil, err
	}
	hd := wlExt.held
	st, err := hd.State()
	var t captureTarget
	obs := observeWlExt(st, hd.Name())
	if err == nil {
		t, err = wlExtTarget(hd, obs, b.Label(), mapWlExtTablet(b.Label(), hd))
	}
	if err != nil {
		stopHeldWlExtLocked()
		releaseWlExtLocked(gen)
		return nil, err
	}
	d := &wlExtDisplay{backend: b, held: hd}
	d.init(gen, b.Label()+" extended display", w, h, t)
	go d.watch(obs)
	return d, nil
}

func (d *wlExtDisplay) Description() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.target
	return fmt.Sprintf("%s extended display %s (%dx%d at +%d+%d)", d.backend.Label(), d.held.Name(), t.Width, t.Height, t.Rect.X, t.Rect.Y)
}

// Refresh re-reads the output's place in the layout.
func (d *wlExtDisplay) Refresh() (captureTarget, error) {
	d.mu.Lock()
	hd, mapped := d.held, d.target.TabletMapped
	d.mu.Unlock()
	if chanClosed(hd.Closed()) {
		return captureTarget{}, fmt.Errorf("%s extended display %s was removed", d.backend.Label(), hd.Name())
	}
	st, err := hd.State()
	if err != nil {
		throttledLog("wlext-refresh", "%s extended display: could not read the outputs (%v); keeping the last layout", d.backend.Label(), err)
		return d.Target(), nil
	}
	t, err := wlExtTarget(hd, observeWlExt(st, hd.Name()), d.backend.Label(), mapped)
	if err != nil {
		return captureTarget{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.held == hd {
		d.target = t
	}
	return d.target, nil
}

// Recover replaces the output after repeated capture failures.
func (d *wlExtDisplay) Recover() error {
	wlExt.mu.Lock()
	defer wlExt.mu.Unlock()
	if err := wlExt.recoverableLocked(d.gen, "extended display"); err != nil {
		return err
	}
	w, h := d.beginRecover()
	defer d.endRecover()
	d.mu.Lock()
	old := d.held
	d.mu.Unlock()
	if wlExt.held == old {
		stopHeldWlExtLocked()
	}
	if err := ensureWlExtLocked(d.backend, w, h); err != nil {
		return err
	}
	hd := wlExt.held
	st, err := hd.State()
	if err != nil {
		return err
	}
	t, err := wlExtTarget(hd, observeWlExt(st, hd.Name()), d.backend.Label(), mapWlExtTablet(d.backend.Label(), hd))
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.held, d.target = hd, t
	d.mu.Unlock()
	d.notifySwapped()
	return nil
}

func (d *wlExtDisplay) Close() {
	d.close(func() {
		wlExt.mu.Lock()
		releaseWlExtLocked(d.gen)
		wlExt.mu.Unlock()
	})
}

// replacing reports whether Recover is replacing hd.
func (d *wlExtDisplay) replacing(hd wlExtHandle) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.recovering || d.held != hd
}

// watch follows the output while the session runs, like the X11 extended
// display's watcher. base is the state the session started from.
func (d *wlExtDisplay) watch(base wlExtObserved) {
	defer close(d.watchDone)
	label := d.backend.Label()
	d.mu.Lock()
	hd := d.held
	d.mu.Unlock()
	closed, changed := hd.Closed(), hd.Changed()
	poll := time.NewTicker(wlExtPoll)
	defer poll.Stop()
	var settle *time.Timer
	var settled <-chan time.Time
	defer func() {
		if settle != nil {
			settle.Stop()
		}
	}()
	arm := func() {
		if settle != nil {
			settle.Stop()
		}
		settle = time.NewTimer(wlExtSettle)
		settled = settle.C
	}
	for {
		select {
		case <-d.stopWatch:
			return
		case <-d.swapped:
			d.mu.Lock()
			hd = d.held
			d.mu.Unlock()
			closed, changed = hd.Closed(), hd.Changed()
			if st, err := hd.State(); err == nil {
				base = observeWlExt(st, hd.Name())
			}
			continue
		case <-closed:
			if d.replacing(hd) {
				closed = nil
				continue
			}
			log.Printf("%s extended display %s was removed; restarting video session", label, hd.Name())
			restartActiveVideoSession()
			return
		case <-changed:
			arm()
			continue
		case <-poll.C:
			if settled == nil {
				if st, err := hd.State(); err == nil && observeWlExt(st, hd.Name()) != base {
					arm()
				}
			}
			continue
		case <-settled:
			settled = nil
		}
		if d.replacing(hd) {
			continue
		}
		st, err := hd.State()
		if err != nil {
			throttledLog("wlext-watch", "%s extended display %s: could not read the outputs: %v", label, hd.Name(), err)
			continue
		}
		obs := observeWlExt(st, hd.Name())
		reloaded := obs.Reloads != base.Reloads
		// A hotplug or a configuration reload sets the output up by the
		// compositor's rules; only other changes are the user's.
		d.saveUserResolution(obs.Active, obs.W, obs.H, reloaded || base.Outputs != obs.Outputs)
		base = obs
		if obs.Placed {
			p := obs.Placement
			wlExt.mu.Lock()
			if wlExt.held == hd {
				wlExt.seen = &p
			}
			wlExt.mu.Unlock()
		}
		if reloaded && obs.Active {
			mapped := mapWlExtTablet(label, hd)
			d.mu.Lock()
			d.target.TabletMapped = mapped
			d.mu.Unlock()
		}
		d.mu.Lock()
		if d.recovering || d.held != hd {
			d.mu.Unlock()
			continue
		}
		restart := d.applyChangeLocked(hd.Name(), observedOutput{
			Present: obs.Present, Active: obs.Active, Mirrored: obs.Mirrored, W: obs.W, H: obs.H, Rect: obs.Rect,
		})
		d.mu.Unlock()
		if restart {
			restartActiveVideoSession()
			return
		}
	}
}
