//go:build windows

package main

import (
	"slices"
	"testing"
)

func TestWindowsCaptureNormalizesPTSAndUsesLocalCursor(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:    captureDDAGrab,
		Adapter: 0,
		Output:  3,
	}, 60, true, false, "h264_qsv", true)

	i := slices.Index(args, "-filter_complex")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("capture args missing -filter_complex: %v", args)
	}
	want := "ddagrab=output_idx=3:framerate=60:draw_mouse=0:dup_frames=1,hwdownload,format=bgra,setpts=N/(60*TB)"
	if args[i+1] != want {
		t.Fatalf("capture filter = %q, want %q", args[i+1], want)
	}
}

func TestWindowsCaptureCanEmbedCursor(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:   captureDDAGrab,
		Output: 3,
	}, 60, false, false, "h264_nvenc", true)

	i := slices.Index(args, "-filter_complex")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("capture args missing -filter_complex: %v", args)
	}
	want := "ddagrab=output_idx=3:framerate=60:draw_mouse=1:dup_frames=0,hwdownload,format=bgra"
	if args[i+1] != want {
		t.Fatalf("capture filter = %q, want %q", args[i+1], want)
	}
}

func TestWindowsCaptureSelectsSecondaryAdapter(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:    captureDDAGrab,
		Adapter: 2,
		Output:  1,
	}, 60, true, false, "h264_nvenc", true)

	i := slices.Index(args, "-init_hw_device")
	if i < 0 || i+1 >= len(args) || args[i+1] != "d3d11va:2" {
		t.Fatalf("capture args = %v, want secondary D3D11 adapter", args)
	}
}

func TestWindowsCaptureZeroCopyNVENC(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:   captureDDAGrab,
		Output: 5,
	}, 60, true, true, "h264_nvenc", true)

	i := slices.Index(args, "-filter_complex")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("capture args missing -filter_complex: %v", args)
	}
	want := "ddagrab=output_idx=5:framerate=60:draw_mouse=0:dup_frames=0,hwmap=derive_device=cuda:mode=direct,scale_cuda=format=nv12"
	if args[i+1] != want {
		t.Fatalf("capture filter = %q, want %q", args[i+1], want)
	}
}

func TestWindowsCaptureOmitsDupFramesWhenUnsupported(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:   captureDDAGrab,
		Output: 2,
	}, 60, false, false, "h264_qsv", false)

	i := slices.Index(args, "-filter_complex")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("capture args missing -filter_complex: %v", args)
	}
	want := "ddagrab=output_idx=2:framerate=60:draw_mouse=1,hwdownload,format=bgra,setpts=N/(60*TB)"
	if args[i+1] != want {
		t.Fatalf("capture filter = %q, want %q", args[i+1], want)
	}
}

func TestWindowsEncoderArgsTemporalAndZeroCopy(t *testing.T) {
	args := windowsEncoderArgs("h264_nvenc", 60, true, true)
	if !slices.Contains(args, "-nonref_p") {
		t.Fatalf("expected -nonref_p in args: %v", args)
	}
	if slices.Contains(args, "yuv420p") {
		t.Fatalf("zero-copy args must not force yuv420p: %v", args)
	}
}
