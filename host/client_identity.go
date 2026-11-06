package main

import (
	"context"
	"slices"
	"sort"
	"strings"
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
	// Recommended maximum resolution reported by the tablet (0 = none).
	RecommendedWidth  int    `json:"recommendedWidth,omitempty"`
	RecommendedHeight int    `json:"recommendedHeight,omitempty"`
	RecommendedReason string `json:"recommendedReason,omitempty"`
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
	updateClientSessions(sessionID, func(ident *clientIdentity) {
		if w > 0 && h > 0 {
			ident.PrefWidth, ident.PrefHeight = w, h
		}
		if fps > 0 {
			ident.PrefFPS = fps
		}
		ident.PrefsPending = false
	})
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
	updateClientSessions(0, func(ident *clientIdentity) {
		ident.PrefWidth, ident.PrefHeight = w, h
		if fps > 0 {
			ident.PrefFPS = fps
		}
	})
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

// normalized trims the text fields, fills in "unknown" placeholders and drops
// implausible sizes and refresh rates.
func (ident clientIdentity) normalized() clientIdentity {
	ident.Platform = strings.TrimSpace(ident.Platform)
	ident.Type = strings.TrimSpace(ident.Type)
	ident.Version = strings.TrimSpace(ident.Version)
	if ident.Platform == "" {
		ident.Platform = "unknown"
	}
	if ident.Type == "" {
		ident.Type = "unknown"
	}
	ident.NativeWidth = max(ident.NativeWidth, 0)
	ident.NativeHeight = max(ident.NativeHeight, 0)
	ident.RefreshRates = normalizeRefreshRates(ident.RefreshRates)
	return ident
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
