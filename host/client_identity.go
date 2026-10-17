package main

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type clientIdentity struct {
	Platform     string `json:"platform"`
	Type         string `json:"type"`
	Version      string `json:"version"`
	NativeWidth  int    `json:"nativeWidth,omitempty"`
	NativeHeight int    `json:"nativeHeight,omitempty"`
	// RefreshRates the tablet panel supports (Hz, ascending, deduplicated).
	RefreshRates []int `json:"refreshRates,omitempty"`
	// Mode the tablet remembered from a previous session (0 = none).
	PrefWidth  int `json:"prefWidth,omitempty"`
	PrefHeight int `json:"prefHeight,omitempty"`
	PrefFPS    int `json:"prefFps,omitempty"`
	// PrefsPending: the tablet keeps per-host preferences and has not yet
	// answered hostInfo with its modePrefs for this host.
	PrefsPending bool `json:"-"`
}

// bindSessionModePrefs applies the tablet's remembered mode for this host.
func bindSessionModePrefs(sessionID uint64, w, h, fps int) {
	clientIdentityState.mu.Lock()
	defer clientIdentityState.mu.Unlock()
	ident, ok := clientIdentityState.session[sessionID]
	if !ok {
		return
	}
	if w > 0 && h > 0 {
		ident.PrefWidth, ident.PrefHeight = w, h
	}
	if fps > 0 {
		ident.PrefFPS = fps
	}
	ident.PrefsPending = false
	clientIdentityState.session[sessionID] = ident
	clientIdentityState.active = ident
}

// clientHelloWait bounds how long a new video session waits for the input
// channel's hello before falling back to the host settings.
const clientHelloWait = 3 * time.Second

func waitForClientIdentity(ctx context.Context, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	ready := func() bool {
		ident := activeClientIdentity()
		return ident.Platform != "" && !ident.PrefsPending
	}
	for !ready() && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// defaultClientFPS is the capture rate used until a tablet has a stored
// preference and when it reports no refresh rates.
const defaultClientFPS = 60

// firstConnectFPS is the rate for a tablet with no stored preference: its
// panel's highest refresh rate at native size (so every presented frame is a
// fresh one), bounded by what this platform can capture, else 60.
func firstConnectFPS(ident clientIdentity) int {
	hz := ident.maxRefreshRate()
	if hz <= 0 {
		return defaultClientFPS
	}
	if hz > platformFirstConnectFPSCeiling {
		hz = platformFirstConnectFPSCeiling
	}
	if hz < defaultClientFPS {
		return defaultClientFPS
	}
	return hz
}

// setClientPrefMode records a mode the user chose (OS display settings or web
// UI) so a session restart keeps it. fps == 0 leaves the rate unchanged.
func setClientPrefMode(w, h, fps int) {
	clientIdentityState.mu.Lock()
	defer clientIdentityState.mu.Unlock()
	apply := func(ident clientIdentity) clientIdentity {
		ident.PrefWidth, ident.PrefHeight = w, h
		if fps > 0 {
			ident.PrefFPS = fps
		}
		return ident
	}
	for id, ident := range clientIdentityState.session {
		clientIdentityState.session[id] = apply(ident)
	}
	if len(clientIdentityState.session) > 0 {
		clientIdentityState.active = apply(clientIdentityState.active)
	}
}

// applyClientDisplayDefaults sets the session's resolution and frame rate from
// the tablet: its remembered mode, or on first connect its native resolution
// at 60 fps. Clients that report no native size keep the host settings.
func applyClientDisplayDefaults(opts *videoOptions, ident clientIdentity) {
	if !strings.EqualFold(ident.Platform, "android") || ident.NativeWidth <= 0 || ident.NativeHeight <= 0 {
		return
	}
	opts.Width, opts.Height = ident.NativeWidth, ident.NativeHeight
	if ident.PrefWidth > 0 && ident.PrefHeight > 0 {
		opts.Width, opts.Height = ident.PrefWidth, ident.PrefHeight
	}
	opts.FPS = firstConnectFPS(ident)
	if ident.PrefFPS > 0 {
		opts.FPS = ident.PrefFPS
	}
}

var clientIdentityState = struct {
	mu      sync.RWMutex
	session map[uint64]clientIdentity
	active  clientIdentity
}{
	session: make(map[uint64]clientIdentity),
}

func bindSessionClientIdentity(sessionID uint64, ident clientIdentity) {
	ident.Platform = strings.TrimSpace(ident.Platform)
	ident.Type = strings.TrimSpace(ident.Type)
	ident.Version = strings.TrimSpace(ident.Version)
	if ident.Platform == "" {
		ident.Platform = "unknown"
	}
	if ident.Type == "" {
		ident.Type = "unknown"
	}
	if ident.NativeWidth < 0 {
		ident.NativeWidth = 0
	}
	if ident.NativeHeight < 0 {
		ident.NativeHeight = 0
	}
	ident.RefreshRates = normalizeRefreshRates(ident.RefreshRates)
	clientIdentityState.mu.Lock()
	clientIdentityState.session[sessionID] = ident
	clientIdentityState.active = ident
	clientIdentityState.mu.Unlock()
}

func unbindSessionClientIdentity(sessionID uint64) {
	clientIdentityState.mu.Lock()
	delete(clientIdentityState.session, sessionID)
	if len(clientIdentityState.session) == 0 {
		clientIdentityState.active = clientIdentity{}
	}
	clientIdentityState.mu.Unlock()
}

func activeClientIdentity() clientIdentity {
	clientIdentityState.mu.RLock()
	defer clientIdentityState.mu.RUnlock()
	return clientIdentityState.active
}

// Bounds for refresh rates accepted from a client.
const (
	minClientRefreshHz = 24
	maxClientRefreshHz = 240
)

// normalizeRefreshRates drops implausible values and returns the rates
// sorted ascending without duplicates (nil when none remain).
func normalizeRefreshRates(rates []int) []int {
	var out []int
	for _, r := range rates {
		if r < minClientRefreshHz || r > maxClientRefreshHz {
			continue
		}
		out = append(out, r)
	}
	sort.Ints(out)
	return slices.Compact(out)
}

// maxRefreshRate is the tablet panel's highest refresh rate, or 0 when the
// client did not report one.
func (c clientIdentity) maxRefreshRate() int {
	if len(c.RefreshRates) == 0 {
		return 0
	}
	return c.RefreshRates[len(c.RefreshRates)-1]
}
