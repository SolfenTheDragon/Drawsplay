//go:build linux

package main

// wlOutputs follows a Wayland compositor's outputs (wl_output, with
// xdg-output's logical layout) as they come, change and go. A compositor
// stops advertising an output it switches off, so only the outputs that show
// part of the desktop are listed.

import (
	"sort"
	"sync"
)

const (
	wlOutputVersion      = 4
	xdgOutputMgrVersion  = 3
	wlOutputModeCurrent  = 0x1
	wlOutputTransform90  = 1
	wlOutputTransform270 = 3
	wlOutputFlipped90    = 5
	wlOutputFlipped270   = 7
	wlOutputReleaseSince = 3
)

// wlOutputInfo is one output as the compositor describes it.
type wlOutputInfo struct {
	Name string
	// Rect is the output's place in the compositor's layout, in logical
	// (scaled) coordinates.
	Rect         displayRect
	ModeW, ModeH int // current mode, in pixels
	Transform    int
	Scale        int
}

// wlTransformSwapsAxes reports whether a wl_output transform turns the picture by 90
// or 270 degrees.
func wlTransformSwapsAxes(t int) bool {
	return t == wlOutputTransform90 || t == wlOutputTransform270 || t == wlOutputFlipped90 || t == wlOutputFlipped270
}

type wlOutputEntry struct {
	id, version uint32
	xdgID       uint32
	// next collects events until the compositor marks them done.
	next, cur    wlOutputInfo
	geomX, geomY int
	logical      bool // xdg-output describes the logical layout
	ready        bool
}

type wlOutputs struct {
	c *wlConn

	mu      sync.Mutex
	xdgMgr  uint32
	entries map[uint32]*wlOutputEntry // by global name
	changed chan struct{}
}

// watchWlOutputs starts following c's outputs and returns once their
// current state is known.
func watchWlOutputs(c *wlConn) (*wlOutputs, *wlRegistry, error) {
	o := &wlOutputs{c: c, entries: map[uint32]*wlOutputEntry{}, changed: make(chan struct{}, 1)}
	reg, err := c.getRegistry(o.global)
	if err != nil {
		return nil, nil, err
	}
	// The globals arrived with getRegistry's roundtrip; this one brings the
	// outputs' state.
	if err := c.roundtrip(); err != nil {
		return nil, nil, err
	}
	return o, reg, nil
}

// Changed is notified (capacity 1) whenever an output comes, changes or goes.
func (o *wlOutputs) Changed() <-chan struct{} { return o.changed }

func (o *wlOutputs) notify() {
	select {
	case o.changed <- struct{}{}:
	default:
	}
}

// Snapshot lists the outputs, oldest first.
func (o *wlOutputs) Snapshot() []wlOutputInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	names := make([]uint32, 0, len(o.entries))
	for name, e := range o.entries {
		if e.ready && e.cur.Name != "" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	out := make([]wlOutputInfo, 0, len(names))
	for _, name := range names {
		out = append(out, o.entries[name].cur)
	}
	return out
}

// lookup finds the wl_output object of the output called name.
func (o *wlOutputs) lookup(name string) (uint32, wlOutputInfo, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, e := range o.entries {
		if e.ready && e.cur.Name == name {
			return e.id, e.cur, true
		}
	}
	return 0, wlOutputInfo{}, false
}

// global runs on the reader goroutine.
func (o *wlOutputs) global(r *wlRegistry, g wlGlobal, added bool) {
	switch {
	case g.Interface == "zxdg_output_manager_v1" && added:
		id, _, err := r.bind(g, xdgOutputMgrVersion, nil)
		if err != nil {
			return
		}
		o.mu.Lock()
		if o.xdgMgr == 0 {
			o.xdgMgr = id
			for _, e := range o.entries {
				o.getXdgOutputLocked(e)
			}
		}
		o.mu.Unlock()
	case g.Interface == "wl_output" && added:
		e := &wlOutputEntry{}
		id, v, err := r.bind(g, wlOutputVersion, func(op uint16, m *wlMessage) { o.outputEvent(e, op, m) })
		if err != nil {
			return
		}
		o.mu.Lock()
		e.id, e.version = id, v
		o.entries[g.Name] = e
		if o.xdgMgr != 0 {
			o.getXdgOutputLocked(e)
		}
		o.mu.Unlock()
	case g.Interface == "wl_output":
		o.mu.Lock()
		e, ok := o.entries[g.Name]
		delete(o.entries, g.Name)
		o.mu.Unlock()
		if !ok {
			return
		}
		if e.xdgID != 0 {
			_ = o.c.destroy(e.xdgID, 0)
		}
		if e.version >= wlOutputReleaseSince {
			_ = o.c.destroy(e.id, 0)
		}
		o.notify()
	}
}

func (o *wlOutputs) getXdgOutputLocked(e *wlOutputEntry) {
	if e.xdgID != 0 {
		return
	}
	e.xdgID, _ = o.c.create(func(op uint16, m *wlMessage) { o.xdgEvent(e, op, m) }, o.xdgMgr, 1, func(id uint32) []wlArg {
		return []wlArg{wlUint(id), wlUint(e.id)}
	})
}

func (o *wlOutputs) outputEvent(e *wlOutputEntry, op uint16, m *wlMessage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch op {
	case 0: // geometry
		e.geomX, e.geomY = int(m.Int()), int(m.Int())
		m.Int() // physical width
		m.Int() // physical height
		m.Int() // subpixel
		m.Str() // make
		m.Str() // model
		e.next.Transform = int(m.Int())
	case 1: // mode
		flags, w, h := m.Uint(), m.Int(), m.Int()
		if flags&wlOutputModeCurrent != 0 {
			e.next.ModeW, e.next.ModeH = int(w), int(h)
		}
	case 2: // done
		o.commitLocked(e)
	case 3: // scale
		e.next.Scale = int(m.Int())
	case 4: // name
		e.next.Name = m.Str()
	}
}

func (o *wlOutputs) xdgEvent(e *wlOutputEntry, op uint16, m *wlMessage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch op {
	case 0: // logical_position
		e.next.Rect.X, e.next.Rect.Y = int(m.Int()), int(m.Int())
		e.logical = true
	case 1: // logical_size
		e.next.Rect.W, e.next.Rect.H = int(m.Int()), int(m.Int())
		e.logical = true
	case 2: // done (before version 3; wl_output.done since)
		o.commitLocked(e)
	case 3: // name
		if e.next.Name == "" {
			e.next.Name = m.Str()
		}
	}
}

func (o *wlOutputs) commitLocked(e *wlOutputEntry) {
	cur := e.next
	if !e.logical {
		// Without xdg-output: the position from wl_output, the size from
		// the mode, its scale and its transform.
		scale := max(cur.Scale, 1)
		w, h := cur.ModeW/scale, cur.ModeH/scale
		if wlTransformSwapsAxes(cur.Transform) {
			w, h = h, w
		}
		cur.Rect = displayRect{X: e.geomX, Y: e.geomY, W: w, H: h}
	}
	changed := !e.ready || cur != e.cur
	e.cur, e.ready = cur, true
	if changed {
		o.notify()
	}
}
