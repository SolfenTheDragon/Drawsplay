//go:build linux

package main

// Linux capture pipeline builder.
//
// PipeWire targets (Mutter virtual monitor) are consumed with GStreamer:
// gst-launch-1.0's pipewiresrc is the reference PipeWire video consumer and
// is preinstalled or one apt install away on Ubuntu (gstreamer1.0-tools +
// gstreamer1.0-plugins-good). Encoder candidates, best first:
//
//	nvh264enc     — NVIDIA NVENC (gstreamer1.0-plugins-bad + NVIDIA driver)
//	vah264enc     — newer VA-API plugin (Intel/AMD, gstreamer1.0-plugins-bad
//	                built with -Dva=enabled)
//	vaapih264enc  — older VA-API plugin (Intel/AMD, gstreamer1.0-vaapi); most
//	                distro packages still ship this one, so it is tried
//	                whenever vah264enc is unavailable.
//	x264enc       — software fallback (gstreamer1.0-plugins-ugly)
//
// The VA-API candidates offload encoding to the GPU, which both improves
// quality at a given bitrate (real motion estimation instead of x264's
// ultrafast preset) and frees the CPU cores the compositor/PipeWire path
// needs, fixing the low-throughput/artifacting symptoms software encoding
// alone produced under CPU pressure.
//
// Every candidate ends in h264parse forcing byte-stream (Annex-B) output on
// stdout via fdsink, matching what video.go expects. Width/height/framerate
// are normalized downstream (videoscale+videorate caps) instead of pinning
// source caps on pipewiresrc, which can fail format negotiation on some
// Mutter/PipeWire stacks and trigger reconnect loops.
//
// X11 targets reuse the ffmpeg x11grab mirror path from M1, with an
// h264_vaapi candidate inserted ahead of the software encoder (see
// vaapiX11Spec).

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// pipewireKeepaliveMs makes pipewiresrc re-send the last frame when the
// compositor emits nothing (no screen damage), so downstream encoders keep
// producing output on a static desktop.
const pipewireKeepaliveMs = 500

// pipewireMinBuffers/pipewireMaxBuffers cap how many buffers pipewiresrc
// negotiates with the PipeWire graph for this stream only (the "quantum"
// lever, scoped per-session rather than a global daemon setting under
// /etc/pipewire or ~/.config/pipewire, which would affect every PipeWire
// client on the machine, audio included). The element's own defaults are
// min=1 (already fine) and max=unlimited, and an unbounded negotiated pool
// lets the compositor's screen-cast source build up a multi-frame backlog
// under load before the downstream leaky queue (see gstPipeWireSpecs) ever
// gets a chance to drop anything. Two buffers is the standard low-latency
// floor (enough to decouple producer/consumer without tolerating queuing
// delay); one is not used because it can starve on a single missed
// wakeup, forcing an avoidable keepalive resend.
const pipewireMinBuffers = 1
const pipewireMaxBuffers = 2

// maxVideoconvertThreads bounds the videoconvert worker count. The stage is
// memory-bandwidth bound, so beyond four workers the extra threads buy no
// throughput while competing with the compositor and the encoder for cores.
const maxVideoconvertThreads = 4

// captureBranchPicosPerPixel is the measured single-threaded cost of the whole
// PipeWire capture branch (pipewiresrc -> videoconvert -> videoscale ->
// videorate -> encoder submission), which videoconvert dominates. Taken from
// in-situ runs on an Intel UHD 620 laptop that were consistent to within 5 %
// across three frame sizes: 9.7 ms/frame at 1920x1080, 17.8 ms at 2560x1440
// and 23.8 ms at 2880x1800, i.e. ~4.7 ns per pixel.
const captureBranchPicosPerPixel = 4700

// videoconvertThreads returns the number of workers videoconvert needs to keep
// the capture branch inside one frame period.
//
// Threading is not free: splitting every frame costs roughly two thirds of a
// core in synchronisation at 60 fps, and measurements showed no throughput
// gain at all when one thread already keeps up (1920x1080 went from 56 % to
// 135 % of a core for the same 60 fps). Above the break-even size it is the
// difference between 43.6 and 55.4 fps at 2560x1440 under load, and between
// 40.4 and 53.0 fps at 2880x1800. So the count is derived from the frame's own
// cost rather than applied unconditionally.
func videoconvertThreads(width, height, fps int) int {
	return videoconvertThreadsFor(width, height, fps, runtime.NumCPU())
}

func videoconvertThreadsFor(width, height, fps, cpus int) int {
	limit := cpus / 2
	if limit > maxVideoconvertThreads {
		limit = maxVideoconvertThreads
	}
	if limit < 1 || width <= 0 || height <= 0 || fps <= 0 {
		return 1
	}
	// Workers needed to fit picosPerPixel * pixels into one frame period,
	// rounded up. 1e12 picoseconds is one second.
	cost := int64(width) * int64(height) * captureBranchPicosPerPixel * int64(fps)
	threads := int((cost + 1e12 - 1) / 1e12)
	if threads < 1 {
		return 1
	}
	if threads > limit {
		return limit
	}
	return threads
}

// buildCapturePipelines returns capture candidates for the target.
func buildCapturePipelines(target captureTarget, opts videoOptions) ([]pipelineSpec, error) {
	switch target.Kind {
	case capturePipeWire:
		return gstPipeWireSpecs(target, opts), nil
	case captureX11Grab:
		display := os.Getenv("DISPLAY")
		if display == "" {
			display = ":0"
		}
		capture := []string{
			"-f", "x11grab",
			"-framerate", fmt.Sprintf("%d", opts.FPS),
			"-i", display,
		}
		specs, err := ffmpegSpecs(opts, capture)
		if err != nil {
			return nil, err
		}
		return withVAAPICandidate(specs, opts, capture), nil
	default:
		return nil, fmt.Errorf("unsupported capture kind on Linux: %s", target.Kind)
	}
}

// gstPipeWireSpecs builds gst-launch-1.0 command lines that pull the virtual
// monitor's PipeWire stream and hardware-encode it to Annex-B on stdout.
//
// Caps handling: keep pipewiresrc unconstrained and normalize
// width/height/framerate downstream. Mutter screen-cast streams negotiate a
// variable framerate (0/1), and forcing source caps can fail negotiation on
// some setups. videorate converts the damage-driven stream to constant
// framerate for the encoder, and pipewiresrc keepalive-time re-emits the last
// frame on a static desktop so the encoder always produces output.
func gstPipeWireSpecs(target captureTarget, opts videoOptions) []pipelineSpec {
	kbps := parseBitrateKbps(opts.Bitrate)
	pipewireFPS := opts.FPS
	if pipewireFPS > 60 {
		// Mutter/PipeWire streams often negotiate up to compositor refresh; on
		// Linux desktops this is typically 60 Hz, and forcing 120 can fail
		// caps negotiation with immediate pipeline teardown.
		pipewireFPS = 60
	}
	gop := pipewireFPS * 2

	// node.latency requests a one-frame-period quantum from the PipeWire
	// graph for this stream specifically (the "props,key=value" form is
	// GstStructure syntax for stream-properties; gst-launch reassembles argv
	// into pipeline description text and parses it, so no shell quoting is
	// needed around the comma). This is the per-stream equivalent of the
	// global pipewire.conf.d quantum knob, without touching any other
	// PipeWire client (including this machine's audio graph).
	src := fmt.Sprintf(
		"pipewiresrc path=%d do-timestamp=true keepalive-time=%d min-buffers=%d max-buffers=%d stream-properties=props,node.latency=1/%d",
		target.Node, pipewireKeepaliveMs, pipewireMinBuffers, pipewireMaxBuffers, pipewireFPS)
	convert := fmt.Sprintf("videoconvert n-threads=%d", videoconvertThreads(target.Width, target.Height, pipewireFPS))
	normalized := fmt.Sprintf(
		"%s ! videoscale ! videorate ! video/x-raw,width=%d,height=%d,framerate=%d/1",
		convert, target.Width, target.Height, pipewireFPS)
	tail := "h264parse config-interval=1 ! video/x-h264,stream-format=byte-stream ! fdsink fd=1 sync=false"

	encoders := []struct {
		name     string
		elements string
	}{
		{
			name: "nvh264enc",
			elements: fmt.Sprintf(
				"nvh264enc preset=low-latency-hq rc-mode=cbr bitrate=%d gop-size=%d bframes=0 zerolatency=true",
				kbps, gop),
		},
		{
			name: "vah264enc",
			elements: fmt.Sprintf(
				"vah264enc rate-control=cbr bitrate=%d key-int-max=%d b-frames=0 target-usage=7",
				kbps, gop),
		},
		{
			name: "vaapih264enc",
			elements: fmt.Sprintf(
				"vaapih264enc rate-control=cbr bitrate=%d keyframe-period=%d max-bframes=0 cabac=true quality-level=7",
				kbps, gop),
		},
		{
			name: "x264enc",
			elements: fmt.Sprintf(
				"video/x-raw,format=I420 ! x264enc tune=zerolatency speed-preset=ultrafast bitrate=%d key-int-max=%d bframes=0 cabac=false byte-stream=true ! video/x-h264,profile=baseline",
				kbps, gop),
		},
	}

	var specs []pipelineSpec
	for _, enc := range encoders {
		// max-size-buffers=1 keeps the capture/encode handoff to at most one
		// frame of slack: enough to decouple the two threads without adding
		// a multi-frame latency buffer (leaky=downstream drops stale frames
		// instead of blocking, so a momentary encoder stall never queues up
		// backlog that would show as increasing lag).
		pipeline := fmt.Sprintf("%s ! %s ! queue max-size-buffers=1 leaky=downstream ! %s ! %s",
			src, normalized, enc.elements, tail)
		specs = append(specs, pipelineSpec{
			Name:        enc.name,
			BaseEncoder: enc.name,
			Cmd:         "gst-launch-1.0",
			Args:        append([]string{"-q"}, strings.Split(pipeline, " ")...),
		})
	}

	// Safety net: software encode with unpinned caps, in case the compositor
	// refuses the requested resolution.
	fallback := fmt.Sprintf("%s ! queue max-size-buffers=1 leaky=downstream ! %s ! video/x-raw,format=I420 ! "+
		"x264enc tune=zerolatency speed-preset=ultrafast bitrate=%d key-int-max=%d bframes=0 cabac=false byte-stream=true ! video/x-h264,profile=baseline ! %s",
		src, convert, kbps, gop, tail)
	specs = append(specs, pipelineSpec{
		Name:        "x264enc-anysize",
		BaseEncoder: "x264enc",
		Cmd:         "gst-launch-1.0",
		Args:        append([]string{"-q"}, strings.Split(fallback, " ")...),
	})
	return specs
}

// --- VA-API candidate for the ffmpeg x11grab path ------------------------
//
// detectEncoders() never offers h264_vaapi, so on Intel/AMD hosts the mirror
// path falls all the way through to software libx264 even when the GPU can
// encode H.264. The candidate below closes that gap on Linux only; the shared
// candidate list, the Windows pipelines and the blackout pipelines are
// untouched.
//
// The colour conversion is done by the GPU's video-processing engine
// (hwupload -> scale_vaapi) rather than by swscale: uploading the capture's
// BGRx frames straight to a VA surface and converting there is what makes the
// candidate cheaper than software encoding. Converting on the CPU first
// (format=nv12,hwupload) costs more than libx264 does, so the filter order
// here is load-bearing, not cosmetic.

const vaapiH264Encoder = "h264_vaapi"

// vaapiScaleFilter carries the GPU-side colour conversion.
const vaapiScaleFilter = "scale_vaapi"

// vaapiRenderNodeGlob locates DRM render nodes; overridden in tests.
var vaapiRenderNodeGlob = "/dev/dri/renderD*"

// vaapiRenderNodes lists the DRM render nodes present, lowest first.
// Replaced in tests so the probe can be exercised without a real /dev/dri.
var vaapiRenderNodes = globVAAPIRenderNodes

func globVAAPIRenderNodes() []string {
	matches, err := filepath.Glob(vaapiRenderNodeGlob)
	if err != nil {
		return nil
	}
	return matches
}

// linuxVAAPIProbe reports the render node to hand ffmpeg and whether this
// ffmpeg build can convert on the GPU. Replaced in tests.
var linuxVAAPIProbe = probeVAAPISupport

// vaapiScaleFilterUsable inspects `ffmpeg -h filter=scale_vaapi` output.
//
// ffmpeg prints "Unknown filter '<name>'." and still **exits 0** for a filter
// it was not built with, so the process exit status says nothing; only the
// text does. The out_range option must be present too, because the generated
// command line passes out_range=tv to keep the encoder's colour range tagged
// correctly, and a build whose scale_vaapi predates that option would fail at
// startup instead of never being offered.
func vaapiScaleFilterUsable(help string) bool {
	if strings.Contains(help, "Unknown filter") {
		return false
	}
	if !strings.Contains(help, vaapiScaleFilter) {
		return false
	}
	return strings.Contains(help, "out_range")
}

func probeVAAPISupport(ffmpeg string) (string, bool) {
	nodes := vaapiRenderNodes()
	if len(nodes) == 0 {
		return "", false
	}
	// Lowest-numbered node, independent of enumeration order: on a hybrid
	// system that is the integrated GPU, which is the one that can encode.
	device := nodes[0]
	for _, node := range nodes[1:] {
		if node < device {
			device = node
		}
	}
	// scale_vaapi carries the colour conversion; without it the candidate
	// would need the software conversion that makes it slower than libx264.
	help, err := ffmpegFilterHelpOutput(ffmpeg, vaapiScaleFilter)
	if err != nil || !vaapiScaleFilterUsable(help) {
		return "", false
	}
	return device, true
}

// withVAAPICandidate inserts an h264_vaapi spec ahead of the first software
// encoder, so every already-working hardware candidate keeps its priority and
// only the software fallback is displaced.
func withVAAPICandidate(specs []pipelineSpec, opts videoOptions, captureArgs []string) []pipelineSpec {
	spec, ok := vaapiX11Spec(opts, captureArgs)
	if !ok {
		return specs
	}
	for _, existing := range specs {
		if existing.BaseEncoder == vaapiH264Encoder {
			return specs
		}
	}
	insertAt := len(specs)
	for i, existing := range specs {
		if strings.HasPrefix(existing.BaseEncoder, "libx") {
			insertAt = i
			break
		}
	}
	out := make([]pipelineSpec, 0, len(specs)+1)
	out = append(out, specs[:insertAt]...)
	out = append(out, spec)
	out = append(out, specs[insertAt:]...)
	return out
}

func vaapiX11Spec(opts videoOptions, captureArgs []string) (pipelineSpec, bool) {
	if strings.EqualFold(opts.Codec, "hevc") || strings.EqualFold(opts.Codec, "h265") {
		return pipelineSpec{}, false
	}
	if isEncoderUnavailable(opts.FFmpeg, vaapiH264Encoder) {
		return pipelineSpec{}, false
	}
	text, err := ffmpegEncodersOutput(opts.FFmpeg)
	if err != nil || !strings.Contains(text, " "+vaapiH264Encoder+" ") {
		return pipelineSpec{}, false
	}
	device, ok := linuxVAAPIProbe(opts.FFmpeg)
	if !ok {
		return pipelineSpec{}, false
	}
	return buildFFmpegSpec(opts, captureArgs, vaapiH264Encoder, vaapiEncoderArgs(device, opts.FPS)), true
}

func vaapiEncoderArgs(device string, fps int) []string {
	gopFrames := fps * 2
	if gopFrames <= 0 {
		gopFrames = 120
	}
	return []string{
		"-vaapi_device", device,
		// out_range=tv tags what the VA post-processor actually emits
		// (limited-range luma); leaving it unset makes ffmpeg advertise full
		// range for the same samples, which washes out the client's picture.
		// probeVAAPISupport refuses to offer this candidate unless the build's
		// scale_vaapi actually has the option.
		"-vf", "hwupload," + vaapiScaleFilter + "=format=nv12:out_range=tv",
		"-rc_mode", "CBR",
		"-bf", "0",
		"-g", fmt.Sprintf("%d", gopFrames),
	}
}
