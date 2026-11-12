//go:build windows

package main

import (
	"os"
	"slices"
	"testing"
)

func TestWindowsNativeHelperCandidateNeedsDriverMonitor(t *testing.T) {
	// Point the lookup at a file that is not a helper: if a case wrongly
	// reached the probe it would fail and log, but must not add a spec.
	t.Setenv(nativeHelperEnv, os.Args[0])
	base := []pipelineSpec{{Name: "h264_nvenc_readback"}}
	cases := map[string]struct {
		target captureTarget
		opts   videoOptions
	}{
		"no driver monitor (Parsec VDD)": {captureTarget{}, videoOptions{Codec: "h264"}},
		"mirrored output":                {captureTarget{IDDMonitorID: 1, Mirrored: true}, videoOptions{Codec: "h264"}},
		"hevc session":                   {captureTarget{IDDMonitorID: 1}, videoOptions{Codec: "hevc"}},
		"helper turned off":              {captureTarget{IDDMonitorID: 1}, videoOptions{Codec: "h264", NativeMedia: "off"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := withWindowsNativeHelperCandidate(base, c.opts, c.target)
			if len(got) != 1 || got[0].Name != base[0].Name {
				t.Fatalf("specs = %+v, want ffmpeg candidates unchanged", got)
			}
		})
	}
}

func TestWindowsNativeHelperArgs(t *testing.T) {
	base := []string{"--monitor-id", "3", "--fps", "120", "--bitrate-kbps", "24000", "--max-inflight", "5"}
	opts := func(features ...string) videoOptions {
		return videoOptions{FPS: 120, Bitrate: "24M", Client: clientSession{Features: normalizeClientFeatures(features)}}
	}

	// An older tablet: periodic keyframes, cursor stays in the frames.
	if args := windowsNativeHelperArgs(captureTarget{IDDMonitorID: 3}, opts("tile1", "meta1")); !slices.Equal(args, base) {
		t.Fatalf("args = %q, want %q", args, base)
	}

	// A tablet that acks frames (and so asks for keyframes) and draws the cursor.
	want := append(slices.Clone(base), "--keyframes", "on-demand", "--cursor", "overlay")
	if args := windowsNativeHelperArgs(captureTarget{IDDMonitorID: 3}, opts("tile1", "meta1", "ack1", "cursor1")); !slices.Equal(args, want) {
		t.Fatalf("args = %q, want %q", args, want)
	}
}

func TestEmbeddedMediaHelperExtracts(t *testing.T) {
	data, err := embeddedMediaHelper.ReadFile(embeddedMediaHelperName)
	if err != nil || len(data) == 0 {
		t.Skip("no helper staged in host/mediahelper (release builds stage it)")
	}
	t.Setenv("LocalAppData", t.TempDir())
	p := platformNativeHelperPath()
	if p == "" {
		t.Fatal("embedded helper was not extracted")
	}
	got, err := os.ReadFile(p)
	if err != nil || !slices.Equal(got, data) {
		t.Fatalf("extracted helper differs from the embedded one (%v)", err)
	}
}
