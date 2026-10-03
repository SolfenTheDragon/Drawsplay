//go:build windows

package main

// Windows capture pipeline builder: ffmpeg ddagrab (DXGI Desktop
// Duplication) of one specific output, encoded by each detected H.264
// encoder in preference order. Frames are downloaded to system memory so
// every encoder candidate — hardware or software — can consume them.

import (
	"fmt"
	"slices"
	"strings"
)

// buildCapturePipelines returns ffmpeg candidates that capture the target
// output, one per detected encoder.
//
// The D3D11 device is left for ddagrab to create itself whenever the target
// sits on the default adapter: an externally created `-init_hw_device`
// device makes IDXGIOutput1::DuplicateOutput fail with E_INVALIDARG on some
// drivers (observed on Intel Iris Xe), while ddagrab's own device — created
// with the flags duplication needs — works. Only a target on a secondary
// adapter forces an explicit device, since ddagrab's self-created device
// always lives on adapter 0.
func buildCapturePipelines(target captureTarget, opts videoOptions) ([]pipelineSpec, error) {
	if target.Kind != captureDDAGrab {
		return nil, fmt.Errorf("unsupported capture kind on Windows: %s", target.Kind)
	}
	encoders := detectEncoders(opts.FFmpeg, opts.Codec)
	if len(encoders) == 0 {
		return nil, fmt.Errorf("no usable %s encoder found in ffmpeg", opts.Codec)
	}
	specs := make([]pipelineSpec, 0, len(encoders)*4)
	dupFramesSupported := supportsDDAGrabDupFrames(opts.FFmpeg)
	for _, enc := range encoders {
		captureVariants := []struct {
			suffix   string
			zeroCopy bool
		}{
			{suffix: "readback", zeroCopy: false},
		}
		// Zero-copy probe first for NVENC: if unsupported on this host/driver
		// it fails fast and we immediately retry the existing readback path.
		if (enc == "h264_nvenc" || enc == "hevc_nvenc") && opts.ZeroCopyCapture && !isZeroCopyBlocked(opts.FFmpeg, enc) {
			captureVariants = append([]struct {
				suffix   string
				zeroCopy bool
			}{{suffix: "zerocopy", zeroCopy: true}}, captureVariants...)
		}
		for _, capVar := range captureVariants {
			if enc == "h264_nvenc" && opts.TemporalLayering && opts.Codec == "h264" {
				temporalSpec := buildFFmpegSpec(
					opts,
					windowsCaptureArgs(target, opts.FPS, opts.LocalCursor, capVar.zeroCopy, enc, dupFramesSupported),
					enc,
					windowsEncoderArgs(enc, opts.FPS, capVar.zeroCopy, true),
				)
				temporalSpec.Name = enc + "_" + capVar.suffix + "_temporal"
				temporalSpec.BaseEncoder = enc
				temporalSpec.SupportsTemporalDrop = true
				specs = append(specs, temporalSpec)
			}
			plainSpec := buildFFmpegSpec(
				opts,
				windowsCaptureArgs(target, opts.FPS, opts.LocalCursor, capVar.zeroCopy, enc, dupFramesSupported),
				enc,
				windowsEncoderArgs(enc, opts.FPS, capVar.zeroCopy, false),
			)
			plainSpec.Name = enc + "_" + capVar.suffix
			plainSpec.BaseEncoder = enc
			specs = append(specs, plainSpec)
		}
	}
	return specs, nil
}

func supportsDDAGrabDupFrames(ffmpeg string) bool {
	help, err := ffmpegFilterHelpOutput(ffmpeg, "ddagrab")
	if err != nil {
		// Conservative default for older ffmpeg builds.
		return false
	}
	return strings.Contains(help, "dup_frames")
}

func windowsCaptureArgs(target captureTarget, fps int, localCursor bool, zeroCopyNVENC bool, encoder string, dupFramesSupported bool) []string {
	var capture []string
	if target.Adapter != 0 {
		capture = append(capture,
			"-init_hw_device", fmt.Sprintf("d3d11va:%d", target.Adapter),
		)
	}
	drawMouse := 1
	if localCursor {
		drawMouse = 0
	}
	useDupFrames := encoder == "h264_qsv" || encoder == "hevc_qsv"
	dupFrames := 0
	if useDupFrames {
		dupFrames = 1
	}
	filter := fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=%d", target.Output, fps, drawMouse)
	if dupFramesSupported {
		filter += fmt.Sprintf(":dup_frames=%d", dupFrames)
	}
	filter += ",hwdownload,format=bgra"
	if useDupFrames {
		filter += fmt.Sprintf(",setpts=N/(%d*TB)", fps)
	}
	if zeroCopyNVENC {
		filter = fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=%d", target.Output, fps, drawMouse)
		if dupFramesSupported {
			filter += ":dup_frames=0"
		}
		filter += ",hwmap=derive_device=cuda:mode=direct,scale_cuda=format=nv12"
	}
	capture = append(capture, "-filter_complex", filter)
	// ddagrab can emit duplicate/non-monotonic PTS values, especially on
	// indirect/virtual displays. Hardware encoders such as QSV key their
	// input-surface lifecycle to those timestamps and may repeatedly encode
	// an old texture while still producing the requested number of frames.
	// Rebuilding PTS from the capture sequence gives every acquired texture a
	// unique, evenly spaced timestamp. Without this, measured 60 fps streams
	// can contain only 1-2 distinct desktop images per second.
	// QSV still needs readback/upload on current ffmpeg builds because
	// ddagrab's dynamic D3D11 pool cannot map into QSV's fixed-size frames.
	return capture
}

func windowsEncoderArgs(encoder string, fps int, zeroCopyNVENC bool, temporalLayering bool) []string {
	args := encoderArgs(encoder, fps)
	if encoder == "h264_nvenc" && temporalLayering {
		args = append(args, "-nonref_p", "1")
	}
	if (encoder != "h264_nvenc" && encoder != "hevc_nvenc") || !zeroCopyNVENC {
		return args
	}
	// Zero-copy NVENC runs on GPU frames; forcing yuv420p here would trigger
	// a download path. Keep NVENC defaults for GPU-native input.
	dropFlags := map[string]bool{"-pix_fmt": true}
	out := make([]string, 0, len(args))
	skipNext := false
	for i := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if dropFlags[args[i]] {
			skipNext = true
			continue
		}
		out = append(out, args[i])
	}
	if !slices.Contains(out, "-tune") {
		out = append(out, "-tune", "ull")
	}
	return out
}
