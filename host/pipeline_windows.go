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
	encoders := preferAdapterEncoders(detectEncoders(opts.FFmpeg, opts.Codec), target.AdapterVendor)
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
		// Zero-copy probe first for NVENC and QSV: if unsupported on this
		// host/driver/ffmpeg build it fails fast and we immediately retry the
		// existing readback path.
		if supportsWindowsZeroCopy(enc) && opts.ZeroCopyCapture && !isZeroCopyBlocked(opts.FFmpeg, enc) {
			captureVariants = append([]struct {
				suffix   string
				zeroCopy bool
			}{{suffix: "zerocopy", zeroCopy: true}}, captureVariants...)
		}
		for _, capVar := range captureVariants {
			if enc == "h264_nvenc" && opts.TemporalLayering && opts.Codec == "h264" {
				temporalSpec := buildFFmpegSpec(
					opts,
					windowsCaptureArgs(target, opts.FPS, capVar.zeroCopy, enc, dupFramesSupported),
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
				windowsCaptureArgs(target, opts.FPS, capVar.zeroCopy, enc, dupFramesSupported),
				enc,
				windowsEncoderArgs(enc, opts.FPS, capVar.zeroCopy, false),
			)
			plainSpec.Name = enc + "_" + capVar.suffix
			plainSpec.BaseEncoder = enc
			specs = append(specs, plainSpec)
		}
	}
	return withAccessUnitWrites(withWindowsVBV(specs, opts), opts), nil
}

// PCI vendor IDs reported by DXGI adapters.
const (
	gpuVendorNVIDIA uint32 = 0x10DE
	gpuVendorAMD    uint32 = 0x1002
	gpuVendorAMDAlt uint32 = 0x1022
	gpuVendorIntel  uint32 = 0x8086
)

// preferAdapterEncoders moves the hardware encoder of the GPU that owns the
// captured output to the front, keeping the rest in their default order.
// The default NVENC → AMF → QSV order suits single-GPU machines, but on
// hybrid laptops (Intel iGPU + NVIDIA/AMD dGPU) the virtual display often
// lives on the iGPU: NVENC's zero-copy mapping then fails and its readback
// variant "succeeds" at a fraction of the frame rate QSV zero-copy reaches,
// so the capture GPU's encoder must be tried first. Unknown vendors (e.g.
// the Microsoft Basic Render Driver) keep the default order.
func preferAdapterEncoders(encoders []string, vendor uint32) []string {
	var family string
	switch vendor {
	case gpuVendorNVIDIA:
		family = "_nvenc"
	case gpuVendorAMD, gpuVendorAMDAlt:
		family = "_amf"
	case gpuVendorIntel:
		family = "_qsv"
	default:
		return encoders
	}
	ordered := make([]string, 0, len(encoders))
	for _, enc := range encoders {
		if strings.HasSuffix(enc, family) {
			ordered = append(ordered, enc)
		}
	}
	for _, enc := range encoders {
		if !strings.HasSuffix(enc, family) {
			ordered = append(ordered, enc)
		}
	}
	return ordered
}

// withWindowsVBV replaces the shared ~1.25-frame VBV (rateControlArgs) with
// 125 ms of stream. At high refresh rates the shared size is a fraction of
// what a full-resolution intra picture needs (231 kbit at 26.7 Mbps and
// 144 fps), so every keyframe — every two seconds and after each encoder
// restart — came out blocky and smeared until P-frames refined it. 125 ms
// lets keyframes keep their quality while bounding the burst on the link
// (the same fix as x11VBVKbits on Linux).
func withWindowsVBV(specs []pipelineSpec, opts videoOptions) []pipelineSpec {
	vbv := fmt.Sprintf("%dk", max(parseBitrateKbps(opts.Bitrate)/8, 100))
	for i := range specs {
		args := slices.Clone(specs[i].Args)
		for j := 0; j+1 < len(args); j++ {
			if args[j] == "-bufsize" {
				args[j+1] = vbv
			}
		}
		specs[i].Args = args
	}
	return specs
}

func supportsDDAGrabDupFrames(ffmpeg string) bool {
	help, err := ffmpegFilterHelpOutput(ffmpeg, "ddagrab")
	if err != nil {
		// Conservative default for older ffmpeg builds.
		return false
	}
	return strings.Contains(help, "dup_frames")
}

func supportsWindowsZeroCopy(encoder string) bool {
	return isNVENCEncoder(encoder) || isQSVEncoder(encoder)
}

func isNVENCEncoder(encoder string) bool {
	return encoder == "h264_nvenc" || encoder == "hevc_nvenc"
}

func isQSVEncoder(encoder string) bool {
	return encoder == "h264_qsv" || encoder == "hevc_qsv"
}

func windowsCaptureArgs(target captureTarget, fps int, zeroCopy bool, encoder string, dupFramesSupported bool) []string {
	var capture []string
	if target.Adapter != 0 {
		capture = append(capture,
			"-init_hw_device", fmt.Sprintf("d3d11va:%d", target.Adapter),
		)
	}
	useDupFrames := isQSVEncoder(encoder)
	dupFrames := 0
	if useDupFrames {
		dupFrames = 1
	}
	filter := fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=1", target.Output, fps)
	if dupFramesSupported {
		filter += fmt.Sprintf(":dup_frames=%d", dupFrames)
	}
	filter += ",hwdownload,format=bgra"
	if useDupFrames {
		filter += fmt.Sprintf(",setpts=N/(%d*TB)", fps)
	}
	if zeroCopy && isNVENCEncoder(encoder) {
		filter = fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=1", target.Output, fps)
		if dupFramesSupported {
			filter += ":dup_frames=0"
		}
		filter += ",hwmap=derive_device=cuda:mode=direct,scale_cuda=format=nv12"
	}
	if zeroCopy && isQSVEncoder(encoder) {
		// Map the D3D11 texture into QSV and convert BGRA→NV12 on the GPU,
		// skipping the CPU readback/upload that caps 2.7K capture near
		// 30 fps on Intel iGPUs. Needs ffmpeg 7+; older builds fail with
		// "Only fixed-size pools can be mapped to QSV frames" and the
		// readback variant takes over.
		filter = fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=1", target.Output, fps)
		if dupFramesSupported {
			filter += ":dup_frames=1"
		}
		filter += fmt.Sprintf(",hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12,setpts=N/(%d*TB)", fps)
	}
	capture = append(capture, "-filter_complex", filter)
	// ddagrab can emit duplicate/non-monotonic PTS values, especially on
	// indirect/virtual displays. Hardware encoders such as QSV key their
	// input-surface lifecycle to those timestamps and may repeatedly encode
	// an old texture while still producing the requested number of frames.
	// Rebuilding PTS from the capture sequence gives every acquired texture a
	// unique, evenly spaced timestamp. Without this, measured 60 fps streams
	// can contain only 1-2 distinct desktop images per second.
	return capture
}

func windowsEncoderArgs(encoder string, fps int, zeroCopy bool, temporalLayering bool) []string {
	args := encoderArgs(encoder, fps)
	if encoder == "h264_nvenc" && temporalLayering {
		args = append(args, "-nonref_p", "1")
	}
	if !zeroCopy || !supportsWindowsZeroCopy(encoder) {
		return args
	}
	// Zero-copy encoders run on GPU frames; forcing a software pixel format
	// here would trigger a download path.
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
	if isNVENCEncoder(encoder) && !slices.Contains(out, "-tune") {
		out = append(out, "-tune", "ull")
	}
	return out
}
