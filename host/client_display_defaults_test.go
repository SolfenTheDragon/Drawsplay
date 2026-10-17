package main

import (
	"slices"
	"testing"
)

func TestClientRefreshRates(t *testing.T) {
	bindSessionClientIdentity(902, clientIdentity{Platform: "android", RefreshRates: []int{120, 60, 0, 999, 60, 24, 96}})
	defer unbindSessionClientIdentity(902)
	got := activeClientIdentity()
	if want := []int{24, 60, 96, 120}; !slices.Equal(got.RefreshRates, want) {
		t.Fatalf("RefreshRates = %v, want %v", got.RefreshRates, want)
	}
	if got.maxRefreshRate() != 120 {
		t.Fatalf("maxRefreshRate = %d, want 120", got.maxRefreshRate())
	}
	if (clientIdentity{}).maxRefreshRate() != 0 {
		t.Fatal("maxRefreshRate without rates should be 0")
	}
}

func TestApplyClientDisplayDefaults(t *testing.T) {
	base := videoOptions{Width: 1920, Height: 1080, FPS: 120}
	android := clientIdentity{Platform: "android", NativeWidth: 2560, NativeHeight: 1600}

	opts := base
	applyClientDisplayDefaults(&opts, android)
	if opts.Width != 2560 || opts.Height != 1600 || opts.FPS != 60 {
		t.Fatalf("first connect = %dx%d@%d, want native 2560x1600@60", opts.Width, opts.Height, opts.FPS)
	}

	panel := android
	panel.RefreshRates = []int{60, 120}
	opts = base
	applyClientDisplayDefaults(&opts, panel)
	if want := min(120, platformFirstConnectFPSCeiling); opts.FPS != want {
		t.Fatalf("first connect on 120 Hz panel = %d fps, want %d", opts.FPS, want)
	}

	android.PrefWidth, android.PrefHeight, android.PrefFPS = 1920, 1200, 90
	opts = base
	applyClientDisplayDefaults(&opts, android)
	if opts.Width != 1920 || opts.Height != 1200 || opts.FPS != 90 {
		t.Fatalf("remembered = %dx%d@%d, want 1920x1200@90", opts.Width, opts.Height, opts.FPS)
	}

	opts = base
	applyClientDisplayDefaults(&opts, clientIdentity{Platform: "windows"})
	if opts != base {
		t.Fatalf("non-android client changed options: %+v", opts)
	}
}

func TestSetClientPrefModeKeepsFPSWhenZero(t *testing.T) {
	bindSessionClientIdentity(900, clientIdentity{Platform: "android", NativeWidth: 2560, NativeHeight: 1600, PrefFPS: 90})
	defer unbindSessionClientIdentity(900)
	setClientPrefMode(1920, 1200, 0)
	got := activeClientIdentity()
	if got.PrefWidth != 1920 || got.PrefHeight != 1200 || got.PrefFPS != 90 {
		t.Fatalf("identity = %+v", got)
	}
}

func TestModePrefsClearPendingAndApply(t *testing.T) {
	bindSessionClientIdentity(901, clientIdentity{Platform: "android", NativeWidth: 2560, NativeHeight: 1600, PrefsPending: true})
	defer unbindSessionClientIdentity(901)
	if !activeClientIdentity().PrefsPending {
		t.Fatal("expected pending prefs after hello")
	}
	bindSessionModePrefs(901, 1920, 1200, 90)
	got := activeClientIdentity()
	if got.PrefsPending || got.PrefWidth != 1920 || got.PrefHeight != 1200 || got.PrefFPS != 90 {
		t.Fatalf("identity = %+v", got)
	}
}

func TestHostIDPersistsAndIsValid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	first := loadOrCreateHostID()
	if !validHostID(first) {
		t.Fatalf("invalid host id %q", first)
	}
	if second := loadOrCreateHostID(); second != first {
		t.Fatalf("host id changed: %q -> %q", first, second)
	}
}
