//go:build windows

package main

import (
	"errors"
	"slices"
	"testing"
)

func TestWindowsCaptureEmbedsCursorAndNormalizesPTS(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:    captureDDAGrab,
		Adapter: 0,
		Output:  3,
	}, 60, false, "h264_qsv", true)

	i := slices.Index(args, "-filter_complex")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("capture args missing -filter_complex: %v", args)
	}
	want := "ddagrab=output_idx=3:framerate=60:draw_mouse=1:dup_frames=1,hwdownload,format=bgra,setpts=N/(60*TB)"
	if args[i+1] != want {
		t.Fatalf("capture filter = %q, want %q", args[i+1], want)
	}
}

func TestPreferAdapterEncodersPutsCaptureGPUEncoderFirst(t *testing.T) {
	all := []string{"h264_nvenc", "h264_amf", "h264_qsv", "libx264"}
	cases := []struct {
		vendor uint32
		want   []string
	}{
		{gpuVendorIntel, []string{"h264_qsv", "h264_nvenc", "h264_amf", "libx264"}},
		{gpuVendorAMD, []string{"h264_amf", "h264_nvenc", "h264_qsv", "libx264"}},
		{gpuVendorNVIDIA, all},
		{0x1414, all}, // Microsoft Basic Render Driver: default order
		{0, all},
	}
	for _, c := range cases {
		if got := preferAdapterEncoders(slices.Clone(all), c.vendor); !slices.Equal(got, c.want) {
			t.Errorf("vendor %#x: got %v, want %v", c.vendor, got, c.want)
		}
	}
	hevc := []string{"hevc_nvenc", "hevc_qsv", "libx265"}
	if got := preferAdapterEncoders(hevc, gpuVendorIntel); !slices.Equal(got, []string{"hevc_qsv", "hevc_nvenc", "libx265"}) {
		t.Errorf("hevc intel order = %v", got)
	}
	if got := preferAdapterEncoders([]string{"libx264"}, gpuVendorAMD); !slices.Equal(got, []string{"libx264"}) {
		t.Errorf("software-only order = %v", got)
	}
}

func TestWindowsCaptureSelectsSecondaryAdapter(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:    captureDDAGrab,
		Adapter: 2,
		Output:  1,
	}, 60, false, "h264_nvenc", true)

	i := slices.Index(args, "-init_hw_device")
	if i < 0 || i+1 >= len(args) || args[i+1] != "d3d11va:2" {
		t.Fatalf("capture args = %v, want secondary D3D11 adapter", args)
	}
}

func TestWindowsCaptureZeroCopyNVENC(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:   captureDDAGrab,
		Output: 5,
	}, 60, true, "h264_nvenc", true)

	i := slices.Index(args, "-filter_complex")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("capture args missing -filter_complex: %v", args)
	}
	want := "ddagrab=output_idx=5:framerate=60:draw_mouse=1:dup_frames=0,hwmap=derive_device=cuda:mode=direct,scale_cuda=format=nv12"
	if args[i+1] != want {
		t.Fatalf("capture filter = %q, want %q", args[i+1], want)
	}
}

func TestWindowsCaptureOmitsDupFramesWhenUnsupported(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:   captureDDAGrab,
		Output: 2,
	}, 60, false, "h264_qsv", false)

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

func TestWindowsCaptureZeroCopyQSV(t *testing.T) {
	args := windowsCaptureArgs(captureTarget{
		Kind:   captureDDAGrab,
		Output: 1,
	}, 120, true, "h264_qsv", true)

	i := slices.Index(args, "-filter_complex")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("capture args missing -filter_complex: %v", args)
	}
	want := "ddagrab=output_idx=1:framerate=120:draw_mouse=1:dup_frames=1,hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12,setpts=N/(120*TB)"
	if args[i+1] != want {
		t.Fatalf("capture filter = %q, want %q", args[i+1], want)
	}
}

func TestWindowsEncoderArgsQSVZeroCopy(t *testing.T) {
	args := windowsEncoderArgs("h264_qsv", 60, true, false)
	if slices.Contains(args, "-pix_fmt") || slices.Contains(args, "-tune") {
		t.Fatalf("QSV zero-copy args must not force a pixel format or NVENC tune: %v", args)
	}
	if args := windowsEncoderArgs("h264_qsv", 60, false, false); !slices.Contains(args, "nv12") {
		t.Fatalf("QSV readback args must keep nv12: %v", args)
	}
}

func TestQSVZeroCopyErrorsBlockOnlyZeroCopy(t *testing.T) {
	err := errors.New("[hwmap] Only fixed-size pools can be mapped to QSV frames.")
	if !isZeroCopyInitError(err) || isPermanentEncoderInitError(err) {
		t.Fatalf("pool mapping failure must block only the zero-copy probe")
	}
}
