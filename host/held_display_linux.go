package main

// The life cycle the Linux extended displays share: X11 (x11_extend_linux.go),
// GNOME (mutter_extend_linux.go) and sway / Hyprland / KWin
// (wl_extend_linux.go).
//
// A virtual output outlives the session that made it by a grace period, so a
// tablet that reconnects at once finds its display where it was. Each
// session claims the output with a new generation number (gen); when it ends
// it releases its claim, and the output is removed after the grace period
// unless another session claimed it meanwhile. On exit the host removes it at
// once.
//
// heldOutput is that process-wide claim. extendSession is one session's use of
// the output: what it captures, the resolution it asked for, and its watcher.
// Each backend embeds both and adds what is particular to it.

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

var errHostExiting = errors.New("host is shutting down")

// heldOutput records which session claims a held virtual output. mu also
// serializes every layout change the host makes on that backend.
type heldOutput struct {
	mu sync.Mutex
	// gen identifies the latest session to claim the output; claimed is
	// whether that session still runs.
	gen     uint64
	claimed bool
	timer   *time.Timer
	exiting bool
}

// claimLocked starts a new session's claim and cancels a pending removal.
func (h *heldOutput) claimLocked() (uint64, error) {
	if h.exiting {
		return 0, errHostExiting
	}
	h.gen++
	h.claimed = true
	h.stopTimerLocked()
	return h.gen, nil
}

// unclaimLocked ends session gen's claim. It reports false when a newer
// session holds the output, which the caller must then leave alone.
func (h *heldOutput) unclaimLocked(gen uint64) bool {
	if gen != h.gen {
		return false
	}
	h.claimed = false
	return true
}

// afterLocked runs fn, with mu held, after delay, unless by then a newer
// session claimed the output, session gen claimed it again, or the host is
// exiting. It replaces any pending run.
func (h *heldOutput) afterLocked(gen uint64, delay time.Duration, fn func()) {
	h.stopTimerLocked()
	h.timer = time.AfterFunc(delay, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if gen != h.gen || h.claimed || h.exiting {
			return
		}
		h.timer = nil
		fn()
	})
}

func (h *heldOutput) stopTimerLocked() {
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

// exitLocked marks the host as exiting: no new claims, no pending removal.
func (h *heldOutput) exitLocked() {
	h.exiting = true
	h.stopTimerLocked()
}

// idleLocked reports whether no session claims the output and the host is
// not exiting, so a finished session's output may be removed early.
func (h *heldOutput) idleLocked() bool { return !h.claimed && !h.exiting }

// recoverableLocked reports why session gen may not replace the output.
func (h *heldOutput) recoverableLocked(gen uint64, label string) error {
	if h.exiting {
		return errHostExiting
	}
	if gen != h.gen {
		return fmt.Errorf("a newer session uses the %s", label)
	}
	return nil
}

// extendSession is one video session's use of a held output. mu guards the
// fields below it and any per-backend field documented as guarded by it.
type extendSession struct {
	gen   uint64
	label string // for log lines, e.g. "GNOME virtual monitor"

	mu         sync.Mutex
	target     captureTarget
	reqW, reqH int // configured resolution
	// recovering: Recover is replacing the output, so its removal does not
	// restart the session.
	recovering bool

	swapped   chan struct{} // Recover replaced the output
	stopWatch chan struct{}
	watchDone chan struct{}
	closeOnce sync.Once
}

func (s *extendSession) init(gen uint64, label string, w, h int, t captureTarget) {
	s.gen, s.label = gen, label
	s.target, s.reqW, s.reqH = t, w, h
	s.swapped = make(chan struct{}, 1)
	s.stopWatch = make(chan struct{})
	s.watchDone = make(chan struct{})
}

func (s *extendSession) Target() captureTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

// close stops the watcher, then runs release once.
func (s *extendSession) close(release func()) {
	s.closeOnce.Do(func() {
		close(s.stopWatch)
		<-s.watchDone
		release()
	})
}

// beginRecover marks a recovery and returns the resolution to recreate.
// endRecover must follow.
func (s *extendSession) beginRecover() (w, h int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recovering = true
	return s.reqW, s.reqH
}

func (s *extendSession) endRecover() {
	s.mu.Lock()
	s.recovering = false
	s.mu.Unlock()
}

// notifySwapped tells the watcher that Recover put a new output in place.
func (s *extendSession) notifySwapped() {
	select {
	case s.swapped <- struct{}{}:
	default:
	}
}

// saveUserResolution saves a w x h the user picked in the desktop's display
// settings as the configured resolution, so the next session keeps it. auto:
// the compositor picked it itself (after a hotplug or a configuration
// reload), which is not saved.
func (s *extendSession) saveUserResolution(active bool, w, h int, auto bool) {
	s.mu.Lock()
	reqW, reqH := s.reqW, s.reqH
	if !active || w <= 0 || h <= 0 || (w == reqW && h == reqH) || auto {
		s.mu.Unlock()
		return
	}
	s.reqW, s.reqH = w, h
	s.mu.Unlock()
	if globalSettingsStore == nil {
		return
	}
	if err := globalSettingsStore.updateResolution(w, h); err != nil {
		log.Printf("Could not save the %s resolution %dx%d: %v", s.label, w, h, err)
		return
	}
	log.Printf("%s resolution changed in the display settings (%dx%d -> %dx%d); saved as the configured resolution", s.label, reqW, reqH, w, h)
}

// observedOutput is what a watcher saw of the output after a change settled.
type observedOutput struct {
	Present, Active, Mirrored bool
	W, H                      int // the size the stream would have
	Rect                      displayRect
}

// applyChangeLocked compares a settled observation of output name with the
// session's target (s.mu held). A move only updates the target, since the
// stream does not depend on where the output is; anything that changes the
// stream (size, switched off, removed, mirroring) needs a new session, which
// it reports with the reason logged.
func (s *extendSession) applyChangeLocked(name string, obs observedOutput) (restart bool) {
	t := s.target
	restart = !obs.Present || !obs.Active || obs.W&^1 != t.Width || obs.H&^1 != t.Height || obs.Mirrored != t.Mirrored
	switch {
	case !restart:
		if obs.Rect != t.Rect {
			s.target.Rect = obs.Rect
			log.Printf("%s %s moved to %dx%d+%d+%d", s.label, name, obs.Rect.W, obs.Rect.H, obs.Rect.X, obs.Rect.Y)
		}
		return false
	case !obs.Present:
		log.Printf("%s %s was removed; restarting video session", s.label, name)
	case !obs.Active:
		log.Printf("%s %s was switched off; restarting video session", s.label, name)
	case obs.Mirrored:
		log.Printf("%s %s now mirrors another display at %dx%d; restarting video session", s.label, name, obs.W, obs.H)
	case t.Mirrored:
		log.Printf("%s %s no longer mirrors another display; restarting video session", s.label, name)
	default:
		log.Printf("%s %s is now %dx%d; restarting video session", s.label, name, obs.W, obs.H)
	}
	return true
}
