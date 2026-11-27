package main

import (
	"reflect"
	"slices"
	"testing"
)

func TestClientRefreshRates(t *testing.T) {
	bindTestIdentity(902, clientIdentity{Platform: "android", RefreshRates: []int{120, 60, 0, 999, 60, 24, 96}})
	defer unbindClientSession(902)
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
	if want := min(90, maxCaptureFPS); opts.Width != 1920 || opts.Height != 1200 || opts.FPS != want {
		t.Fatalf("remembered = %dx%d@%d, want 1920x1200@%d", opts.Width, opts.Height, opts.FPS, want)
	}

	opts = base
	applyClientDisplayDefaults(&opts, clientIdentity{Platform: "windows"})
	if !reflect.DeepEqual(opts, base) {
		t.Fatalf("non-android client changed options: %+v", opts)
	}
}

func TestSetClientPrefModeKeepsFPSWhenZero(t *testing.T) {
	bindTestIdentity(900, clientIdentity{Platform: "android", NativeWidth: 2560, NativeHeight: 1600, PrefFPS: 90})
	defer unbindClientSession(900)
	setClientPrefMode(1920, 1200, 0)
	got := activeClientIdentity()
	if got.PrefWidth != 1920 || got.PrefHeight != 1200 || got.PrefFPS != 90 {
		t.Fatalf("identity = %+v", got)
	}
}

func TestModePrefsClearPendingAndApply(t *testing.T) {
	bindTestIdentity(901, clientIdentity{Platform: "android", NativeWidth: 2560, NativeHeight: 1600, PrefsPending: true})
	defer unbindClientSession(901)
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

// A hello or modePrefs packet must not size the virtual display beyond what
// a real tablet could have: the USB accessory link is not authenticated and
// the Linux backends create whatever size they are given.
func TestImplausibleClientModesAreDropped(t *testing.T) {
	bindTestIdentity(903, clientIdentity{
		Platform: "android", NativeWidth: 65535, NativeHeight: 65535,
		PrefWidth: 100000, PrefHeight: 1200,
	})
	defer unbindClientSession(903)
	got := activeClientIdentity()
	if got.NativeWidth != 0 || got.NativeHeight != 0 || got.PrefWidth != 0 || got.PrefHeight != 0 {
		t.Fatalf("implausible sizes kept: %+v", got)
	}
	opts := videoOptions{Width: 1920, Height: 1080}
	applyClientDisplayDefaults(&opts, got)
	if opts.Width != 1920 || opts.Height != 1080 {
		t.Fatalf("display = %dx%d, want the host's 1920x1080", opts.Width, opts.Height)
	}

	bindSessionModePrefs(903, 7680, 7680, 60) // each side fits, the area does not
	if got := activeClientIdentity(); got.PrefWidth != 0 || got.PrefHeight != 0 {
		t.Fatalf("modePrefs over 8K kept: %+v", got)
	}
}

func TestPlausibleClientModes(t *testing.T) {
	for _, m := range [][2]int{{2560, 1600}, {1600, 2560}, {7680, 4320}, {4320, 7680}, {320, 480}} {
		if !plausibleClientMode(m[0], m[1]) {
			t.Errorf("%dx%d refused", m[0], m[1])
		}
	}
	for _, m := range [][2]int{{0, 0}, {-1920, 1080}, {319, 480}, {7681, 1080}, {7680, 7680}, {65535, 65535}} {
		if plausibleClientMode(m[0], m[1]) {
			t.Errorf("%dx%d accepted", m[0], m[1])
		}
	}
}
