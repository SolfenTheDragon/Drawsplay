package main

import (
	"errors"
	"reflect"
	"testing"
)

func resetEncoderHealthForTest() {
	encoderHealthMu.Lock()
	encoderUnavailable = map[string]bool{}
	zeroCopyBlocked = map[string]bool{}
	encoderHealthMu.Unlock()
}

func TestPermanentEncoderInitErrorClassification(t *testing.T) {
	for _, message := range []string{
		"[h264_nvenc] Cannot load nvcuda.dll",
		"[h264_amf] DLL amfrt64.dll failed to open",
		"[h264_qsv] Error initializing an internal MFX session: unsupported (-3)",
	} {
		if !isPermanentEncoderInitError(errors.New(message)) {
			t.Fatalf("did not classify %q as permanent encoder init error", message)
		}
	}
	if isPermanentEncoderInitError(errors.New("Error configuring filter graph: Function not implemented")) {
		t.Fatal("capture filter error misclassified as permanent encoder backend failure")
	}
}

func TestZeroCopyInitErrorClassification(t *testing.T) {
	for _, message := range []string{
		"[Parsed_hwmap_1] Failed to created derived device context: -40.",
		"[Parsed_hwmap_1] Failed to configure output pad on Parsed_hwmap_1",
	} {
		if !isZeroCopyInitError(errors.New(message)) {
			t.Fatalf("did not classify %q as zero-copy init error", message)
		}
	}
}

func TestDetectEncodersFiltersRuntimeUnavailable(t *testing.T) {
	resetEncoderHealthForTest()
	ffmpeg := "ffmpeg-test"

	encoderProbeMu.Lock()
	encoderProbeOutput[ffmpeg] = " V..... h264_nvenc \n V..... h264_qsv \n V..... libx264 \n"
	encoderProbeMu.Unlock()

	markEncoderUnavailable(ffmpeg, "h264_nvenc")
	got := detectEncoders(ffmpeg, "h264")
	want := []string{"h264_qsv", "libx264"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectEncoders filtered = %v, want %v", got, want)
	}
}

func TestDetectEncodersFallsBackWhenEverythingFiltered(t *testing.T) {
	resetEncoderHealthForTest()
	ffmpeg := "ffmpeg-test-all-filtered"

	encoderProbeMu.Lock()
	encoderProbeOutput[ffmpeg] = " V..... h264_nvenc \n"
	encoderProbeMu.Unlock()

	markEncoderUnavailable(ffmpeg, "h264_nvenc")
	got := detectEncoders(ffmpeg, "h264")
	want := []string{"h264_nvenc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectEncoders fallback = %v, want %v", got, want)
	}
}

func TestZeroCopyBlockedRuntimeFlag(t *testing.T) {
	resetEncoderHealthForTest()
	ffmpeg := "ffmpeg-test-zero-copy"
	encoder := "h264_nvenc"
	if isZeroCopyBlocked(ffmpeg, encoder) {
		t.Fatal("zero-copy unexpectedly blocked before mark")
	}
	markZeroCopyBlocked(ffmpeg, encoder)
	if !isZeroCopyBlocked(ffmpeg, encoder) {
		t.Fatal("zero-copy block mark not applied")
	}
}
