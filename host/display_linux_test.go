//go:build linux

package main

import (
	"testing"
	"time"
)

func TestLinuxSessionBackend(t *testing.T) {
	tests := []struct {
		name           string
		sessionType    string
		display        string
		waylandDisplay string
		want           string
	}{
		{name: "explicit x11", sessionType: "x11", display: ":0", waylandDisplay: "wayland-0", want: "x11"},
		{name: "explicit wayland", sessionType: "wayland", display: ":0", waylandDisplay: "wayland-0", want: "wayland"},
		{name: "infer x11", display: ":0", want: "x11"},
		{name: "infer wayland", waylandDisplay: "wayland-0", want: "wayland"},
		{name: "unknown", want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linuxSessionBackend(tt.sessionType, tt.display, tt.waylandDisplay); got != tt.want {
				t.Fatalf("linuxSessionBackend(%q, %q, %q) = %q, want %q",
					tt.sessionType, tt.display, tt.waylandDisplay, got, tt.want)
			}
		})
	}
}

func TestX11MirrorRestartsWhenVirtualOutputAppears(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	restarted := make(chan struct{}, 1)
	go watchX11MirrorForVirtualOutput(
		stop, done, time.Millisecond,
		func() bool { return true },
		func() videoStatusInfo { return videoStatusInfo{Streaming: true, DisplayMode: "mirror"} },
		func() bool { return true },
		func() bool {
			restarted <- struct{}{}
			return true
		},
	)
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("did not restart after the X11 virtual output appeared")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("output watcher did not finish after requesting a restart")
	}
}

func TestX11MirrorWatcherStopsWithoutRestartingUnrelatedSessions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		isX11       bool
		video       videoStatusInfo
		outputReady bool
	}{
		{"wayland", false, videoStatusInfo{Streaming: true, DisplayMode: "mirror"}, true},
		{"not streaming", true, videoStatusInfo{DisplayMode: "mirror"}, true},
		{"already extended", true, videoStatusInfo{Streaming: true, DisplayMode: "extend"}, true},
		{"output unavailable", true, videoStatusInfo{Streaming: true, DisplayMode: "mirror"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stop := make(chan struct{})
			done := make(chan struct{})
			restarted := make(chan struct{}, 1)
			go watchX11MirrorForVirtualOutput(
				stop, done, time.Millisecond,
				func() bool { return tc.isX11 },
				func() videoStatusInfo { return tc.video },
				func() bool { return tc.outputReady },
				func() bool { restarted <- struct{}{}; return true },
			)
			time.Sleep(10 * time.Millisecond)
			close(stop)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("output watcher did not stop")
			}
			select {
			case <-restarted:
				t.Fatal("restarted an unrelated or already-healthy session")
			default:
			}
		})
	}
}
