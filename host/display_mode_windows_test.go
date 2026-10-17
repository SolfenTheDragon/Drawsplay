//go:build windows

package main

import (
	"encoding/binary"
	"path/filepath"
	"testing"
)

// useTempDisplayModeStore points the remembered-mode store at a temp file.
func useTempDisplayModeStore(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "display_modes.json")
	old := windowsDisplayModeStore.path
	windowsDisplayModeStore.path = func() (string, error) { return path, nil }
	t.Cleanup(func() { windowsDisplayModeStore.path = old })
}

func TestDesiredDisplayModePrefersTabletNative(t *testing.T) {
	useTempDisplayModeStore(t)
	const session = 7301
	bindSessionClientIdentity(session, clientIdentity{
		Platform: "android", NativeWidth: 2960, NativeHeight: 1848,
		RefreshRates: []int{60, 120},
		PrefWidth:    1920, PrefHeight: 1080, PrefFPS: 60,
	})
	defer unbindSessionClientIdentity(session)

	w, h, hz, nw, nh := desiredDisplayMode(videoOptions{Width: 1920, Height: 1080, FPS: 60})
	if w != 2960 || h != 1848 || hz != 120 {
		t.Fatalf("mode = %dx%d@%d, want 2960x1848@120 (tablet native, not remembered 1080p)", w, h, hz)
	}
	if nw != 2960 || nh != 1848 {
		t.Fatalf("native = %dx%d, want 2960x1848", nw, nh)
	}
}

func TestDesiredDisplayModeUsesWindowsChoice(t *testing.T) {
	useTempDisplayModeStore(t)
	const session = 7303
	bindSessionClientIdentity(session, clientIdentity{
		Platform: "android", NativeWidth: 2960, NativeHeight: 1848,
		RefreshRates: []int{60, 120},
	})
	defer unbindSessionClientIdentity(session)

	if err := rememberWindowsDisplayMode(2960, 1848, 2560, 1440); err != nil {
		t.Fatal(err)
	}
	w, h, _, nw, nh := desiredDisplayMode(videoOptions{})
	if w != 2560 || h != 1440 || nw != 2960 || nh != 1848 {
		t.Fatalf("mode = %dx%d native %dx%d, want 2560x1440 native 2960x1848", w, h, nw, nh)
	}
	if _, _, ok := rememberedWindowsDisplayMode(1920, 1200); ok {
		t.Fatal("remembered mode leaked to a different tablet")
	}
	// Picking native again clears the record.
	if err := rememberWindowsDisplayMode(2960, 1848, 2960, 1848); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := rememberedWindowsDisplayMode(2960, 1848); ok {
		t.Fatal("native choice should clear the remembered mode")
	}
}

func TestIDDDisplayIgnoresRotationAsModeChoice(t *testing.T) {
	useTempDisplayModeStore(t)
	d := &iddDisplay{width: 2960, height: 1848, nativeWidth: 2960, nativeHeight: 1848}
	d.noteUserMode(captureTarget{Width: 1848, Height: 2960})
	if _, _, ok := rememberedWindowsDisplayMode(2960, 1848); ok {
		t.Fatal("portrait rotation was remembered as a resolution choice")
	}
	d.noteUserMode(captureTarget{Width: 1280, Height: 720})
	if w, h, ok := rememberedWindowsDisplayMode(2960, 1848); !ok || w != 1280 || h != 720 {
		t.Fatalf("remembered = %dx%d ok=%v, want 1280x720", w, h, ok)
	}
	if d.width != 1280 || d.height != 720 {
		t.Fatalf("recovery mode = %dx%d, want 1280x720", d.width, d.height)
	}
}

func TestEncodeIDDAddMonitorV2Layout(t *testing.T) {
	in := encodeIDDAddMonitor(2560, 1440, 120, 2960, 1848)
	want := []uint32{2560, 1440, 120, 2960, 1848}
	for i, v := range want {
		if got := binary.LittleEndian.Uint32(in[i*4:]); got != v {
			t.Fatalf("field %d = %d, want %d", i, got, v)
		}
	}
}

func TestDesiredDisplayModeFallsBackWithoutNativeSize(t *testing.T) {
	useTempDisplayModeStore(t)
	const session = 7302
	bindSessionClientIdentity(session, clientIdentity{Platform: "windows"})
	defer unbindSessionClientIdentity(session)

	if w, h, _, _, _ := desiredDisplayMode(videoOptions{Width: 2560, Height: 1440}); w != 2560 || h != 1440 {
		t.Fatalf("mode = %dx%d, want host setting 2560x1440", w, h)
	}
	if w, h, _, _, _ := desiredDisplayMode(videoOptions{}); w != 1920 || h != 1080 {
		t.Fatalf("mode = %dx%d, want 1920x1080 fallback", w, h)
	}
}
