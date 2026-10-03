//go:build linux

package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func pipeWireTestOpts() (captureTarget, videoOptions) {
	target := captureTarget{Kind: capturePipeWire, Node: 42, Width: 1920, Height: 1080}
	opts := videoOptions{FPS: 60, Bitrate: "8M"}
	return target, opts
}

func TestGstPipeWireSpecsOrderAndFallback(t *testing.T) {
	target, opts := pipeWireTestOpts()
	specs := gstPipeWireSpecs(target, opts)
	wantNames := []string{"nvh264enc", "vah264enc", "vaapih264enc", "x264enc", "x264enc-anysize"}
	if len(specs) != len(wantNames) {
		t.Fatalf("got %d specs, want %d", len(specs), len(wantNames))
	}
	for i, name := range wantNames {
		if specs[i].Name != name {
			t.Errorf("spec[%d].Name = %q, want %q", i, specs[i].Name, name)
		}
		if specs[i].Cmd != "gst-launch-1.0" {
			t.Errorf("spec[%d].Cmd = %q, want gst-launch-1.0", i, specs[i].Cmd)
		}
	}
	if specs[len(specs)-1].BaseEncoder != "x264enc" {
		t.Errorf("fallback BaseEncoder = %q, want x264enc", specs[len(specs)-1].BaseEncoder)
	}
}

func TestGstPipeWireSpecsDoNotPinFramerateOnSourceCaps(t *testing.T) {
	// Mutter screen-cast streams negotiate a variable framerate (0/1);
	// pinning source caps on pipewiresrc can fail negotiation outright.
	target, opts := pipeWireTestOpts()
	for _, spec := range gstPipeWireSpecs(target, opts) {
		joined := strings.Join(spec.Args, " ")
		if strings.Contains(joined, "pipewiresrc path=42 do-timestamp=true keepalive-time=500 ! video/x-raw,width=") {
			t.Errorf("spec %s pins source caps on pipewiresrc path: %s", spec.Name, joined)
		}
	}
}

func TestGstPipeWireSizedSpecsUseVideorateCFR(t *testing.T) {
	target, opts := pipeWireTestOpts()
	specs := gstPipeWireSpecs(target, opts)
	for _, spec := range specs[:len(specs)-1] {
		joined := strings.Join(spec.Args, " ")
		if !strings.Contains(joined, "videoscale ! videorate ! video/x-raw,width=1920,height=1080,framerate=60/1") {
			t.Errorf("spec %s does not normalize output caps via videoscale/videorate: %s", spec.Name, joined)
		}
		if strings.Contains(joined, "videoconvert ! videoconvert") {
			t.Errorf("spec %s duplicated videoconvert unexpectedly: %s", spec.Name, joined)
		}
	}
}

func TestGstPipeWireSpecsUseKeepalive(t *testing.T) {
	// A static virtual monitor produces no damage frames; keepalive-time
	// keeps the encoder producing output so the start timeout does not trip.
	target, opts := pipeWireTestOpts()
	want := fmt.Sprintf("keepalive-time=%d", pipewireKeepaliveMs)
	for _, spec := range gstPipeWireSpecs(target, opts) {
		joined := strings.Join(spec.Args, " ")
		if !strings.Contains(joined, "pipewiresrc path=42 do-timestamp=true "+want) {
			t.Errorf("spec %s missing pipewiresrc keepalive: %s", spec.Name, joined)
		}
	}
}

func TestGstPipeWireFallbackHasUnpinnedCaps(t *testing.T) {
	target, opts := pipeWireTestOpts()
	specs := gstPipeWireSpecs(target, opts)
	fallback := strings.Join(specs[len(specs)-1].Args, " ")
	if strings.Contains(fallback, "width=") || strings.Contains(fallback, "framerate=60/1") {
		t.Errorf("anysize fallback must not pin caps: %s", fallback)
	}
	if !strings.Contains(fallback, "x264enc") {
		t.Errorf("anysize fallback must software-encode: %s", fallback)
	}
}

func TestVideoconvertThreadsForFrameCost(t *testing.T) {
	// One worker per frame period of measured conversion cost (~4.7 ns per
	// pixel single-threaded): 1080p60 fits in one thread and must stay
	// single-threaded, larger frames do not.
	cases := []struct {
		name            string
		w, h, fps, cpus int
		want            int
	}{
		{name: "720p60 8 cpus", w: 1280, h: 720, fps: 60, cpus: 8, want: 1},
		{name: "1080p60 8 cpus", w: 1920, h: 1080, fps: 60, cpus: 8, want: 1},
		{name: "1440p60 8 cpus", w: 2560, h: 1440, fps: 60, cpus: 8, want: 2},
		{name: "2880x1800p60 8 cpus", w: 2880, h: 1800, fps: 60, cpus: 8, want: 2},
		{name: "2160p60 8 cpus", w: 3840, h: 2160, fps: 60, cpus: 8, want: 3},
		{name: "2160p60 capped by cpus", w: 3840, h: 2160, fps: 60, cpus: 4, want: 2},
		{name: "2160p60 dual core stays serial", w: 3840, h: 2160, fps: 60, cpus: 2, want: 1},
		{name: "huge frame capped at max", w: 7680, h: 4320, fps: 60, cpus: 32, want: maxVideoconvertThreads},
		{name: "1440p30 fits one thread", w: 2560, h: 1440, fps: 30, cpus: 8, want: 1},
		{name: "degenerate size", w: 0, h: 0, fps: 60, cpus: 8, want: 1},
		{name: "degenerate rate", w: 2560, h: 1440, fps: 0, cpus: 8, want: 1},
	}
	for _, tc := range cases {
		if got := videoconvertThreadsFor(tc.w, tc.h, tc.fps, tc.cpus); got != tc.want {
			t.Errorf("%s: videoconvertThreadsFor(%d,%d,%d,%d) = %d, want %d",
				tc.name, tc.w, tc.h, tc.fps, tc.cpus, got, tc.want)
		}
	}
}

func TestGstPipeWireSpecsThreadVideoconvert(t *testing.T) {
	// videoconvert is the single-threaded hot spot of the capture branch;
	// n-threads parallelizes it without changing the negotiated caps.
	target, opts := pipeWireTestOpts()
	target.Width, target.Height = 2880, 1800
	want := fmt.Sprintf("videoconvert n-threads=%d", videoconvertThreads(2880, 1800, 60))
	for _, spec := range gstPipeWireSpecs(target, opts) {
		joined := strings.Join(spec.Args, " ")
		if !strings.Contains(joined, want) {
			t.Errorf("spec %s does not thread videoconvert (%q): %s", spec.Name, want, joined)
		}
	}
}

func TestGstPipeWireSpecsUseClampedRateForThreadBudget(t *testing.T) {
	// The thread budget must follow the rate actually streamed (clamped to
	// 60), not the configured one, or a -fps 120 session over-provisions.
	target, opts := pipeWireTestOpts()
	target.Width, target.Height = 2560, 1440
	opts.FPS = 120
	want := fmt.Sprintf("videoconvert n-threads=%d", videoconvertThreads(2560, 1440, 60))
	for _, spec := range gstPipeWireSpecs(target, opts) {
		if !strings.Contains(strings.Join(spec.Args, " "), want) {
			t.Errorf("spec %s did not size threads from the clamped rate (%q)", spec.Name, want)
		}
	}
}

func TestGstPipeWireSpecsStaySerialAt1080p(t *testing.T) {
	// One thread already sustains 1080p60; threading it measured no fps gain
	// and more than doubled capture CPU (56 % -> 135 % of one core).
	target, opts := pipeWireTestOpts()
	for _, spec := range gstPipeWireSpecs(target, opts) {
		joined := strings.Join(spec.Args, " ")
		if !strings.Contains(joined, "videoconvert n-threads=1") {
			t.Errorf("spec %s should keep 1080p conversion single-threaded: %s", spec.Name, joined)
		}
	}
}

func TestGstPipeWireSpecsKeepBoundedQueue(t *testing.T) {
	target, opts := pipeWireTestOpts()
	for _, spec := range gstPipeWireSpecs(target, opts) {
		joined := strings.Join(spec.Args, " ")
		if !strings.Contains(joined, "queue max-size-buffers=1 leaky=downstream") {
			t.Errorf("spec %s lost its bounded capture/encode queue: %s", spec.Name, joined)
		}
	}
}

func TestGstPipeWireSpecsClampFramerateTo60(t *testing.T) {
	target, opts := pipeWireTestOpts()
	opts.FPS = 120
	specs := gstPipeWireSpecs(target, opts)
	for _, spec := range specs {
		joined := strings.Join(spec.Args, " ")
		if strings.Contains(joined, "framerate=120/1") {
			t.Errorf("spec %s should not force 120fps on PipeWire path: %s", spec.Name, joined)
		}
	}
	first := strings.Join(specs[0].Args, " ")
	if !strings.Contains(first, "framerate=60/1") {
		t.Fatalf("expected PipeWire framerate clamp to 60fps: %s", first)
	}
}

// --- X11 (x11grab) path -------------------------------------------------

func withFakeFFmpegEncoders(t *testing.T, name string, encoders string) string {
	t.Helper()
	encoderProbeMu.Lock()
	encoderProbeOutput[name] = encoders
	encoderProbeMu.Unlock()
	t.Cleanup(func() {
		encoderProbeMu.Lock()
		delete(encoderProbeOutput, name)
		encoderProbeMu.Unlock()
	})
	encoderHealthMu.Lock()
	encoderUnavailable = map[string]bool{}
	encoderHealthMu.Unlock()
	return name
}

func withFakeVAAPIProbe(t *testing.T, node string, ok bool) {
	t.Helper()
	prev := linuxVAAPIProbe
	linuxVAAPIProbe = func(string) (string, bool) { return node, ok }
	t.Cleanup(func() { linuxVAAPIProbe = prev })
}

func x11TestTarget() captureTarget {
	return captureTarget{Kind: captureX11Grab, Width: 1920, Height: 1080}
}

func specNames(specs []pipelineSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func TestEffectiveCaptureFPSClampsX11Grab(t *testing.T) {
	// ffmpeg's x11grab free-runs at ~470-490 fps when asked for 120, which
	// drives congestion-based bitrate-ladder collapse and restart churn.
	// The X11 mirror is bounded by the same 60 Hz desktop refresh the
	// PipeWire path is already clamped to.
	if got := effectiveCaptureFPS(x11TestTarget(), 120); got != 60 {
		t.Fatalf("x11grab fps clamp = %d, want 60", got)
	}
	if got := effectiveCaptureFPS(x11TestTarget(), 60); got != 60 {
		t.Fatalf("x11grab fps passthrough at 60 = %d, want 60", got)
	}
	if got := effectiveCaptureFPS(x11TestTarget(), 30); got != 30 {
		t.Fatalf("x11grab fps passthrough below the clamp = %d, want 30", got)
	}
	if got := effectiveCaptureFPS(captureTarget{Kind: capturePipeWire}, 120); got != 60 {
		t.Fatalf("pipewire fps clamp regressed = %d, want 60", got)
	}
}

func TestBuildCapturePipelinesX11GrabUsesClampedFPS(t *testing.T) {
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-clamp", " V..... libx264 \n")
	withFakeVAAPIProbe(t, "", false)

	target := x11TestTarget()
	opts := videoOptions{FPS: effectiveCaptureFPS(target, 120), Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(target, opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	if len(specs) == 0 {
		t.Fatal("no x11grab specs produced")
	}
	joined := strings.Join(specs[0].Args, " ")
	if !strings.Contains(joined, "-framerate 60 ") {
		t.Errorf("x11grab capture rate not clamped: %s", joined)
	}
	if strings.Contains(joined, "-framerate 120 ") {
		t.Errorf("x11grab still requests 120 fps: %s", joined)
	}
	if !strings.Contains(joined, "-g 120 ") {
		t.Errorf("GOP should follow the clamped rate (60 x 2): %s", joined)
	}
}

func TestX11GrabInsertsVAAPIBeforeSoftwareEncoder(t *testing.T) {
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-vaapi",
		" V..... h264_nvenc \n V..... h264_vaapi \n V..... libx264 \n")
	withFakeVAAPIProbe(t, "/dev/dri/renderD128", true)

	target := x11TestTarget()
	opts := videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(target, opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	got := specNames(specs)
	want := []string{"h264_nvenc", "h264_vaapi", "libx264"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("x11grab candidates = %v, want %v", got, want)
	}
}

func TestX11GrabVAAPISpecUsesGPUColourConversion(t *testing.T) {
	// Uploading BGRx straight to a VA surface and converting with the GPU's
	// VPP avoids the software format conversion that otherwise dominates the
	// ffmpeg path's CPU cost.
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-vaapi-args", " V..... libx264 \n V..... h264_vaapi \n")
	withFakeVAAPIProbe(t, "/dev/dri/renderD128", true)

	target := x11TestTarget()
	opts := videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(target, opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	var vaapi *pipelineSpec
	for i := range specs {
		if specs[i].Name == "h264_vaapi" {
			vaapi = &specs[i]
		}
	}
	if vaapi == nil {
		t.Fatalf("no h264_vaapi candidate in %v", specNames(specs))
	}
	if vaapi.BaseEncoder != "h264_vaapi" {
		t.Errorf("BaseEncoder = %q, want h264_vaapi", vaapi.BaseEncoder)
	}
	joined := strings.Join(vaapi.Args, " ")
	for _, want := range []string{
		"-vaapi_device /dev/dri/renderD128",
		"-vf hwupload,scale_vaapi=format=nv12:out_range=tv",
		"-rc_mode CBR",
		"-bf 0",
		"-g 120",
		"-f h264 pipe:1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("h264_vaapi args missing %q: %s", want, joined)
		}
	}
	// A software output pixel format would defeat the hardware upload.
	if strings.Contains(joined, "-pix_fmt") {
		t.Errorf("h264_vaapi must not force a software pixel format: %s", joined)
	}
}

func TestX11GrabOmitsVAAPIWhenUnsupported(t *testing.T) {
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-no-vaapi",
		" V..... h264_vaapi \n V..... libx264 \n")
	withFakeVAAPIProbe(t, "", false)

	opts := videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(x11TestTarget(), opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	if got := specNames(specs); !reflect.DeepEqual(got, []string{"libx264"}) {
		t.Fatalf("candidates without a render node = %v, want [libx264]", got)
	}
}

func TestX11GrabOmitsVAAPIWhenFFmpegLacksEncoder(t *testing.T) {
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-vaapi-absent", " V..... libx264 \n")
	withFakeVAAPIProbe(t, "/dev/dri/renderD128", true)

	opts := videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(x11TestTarget(), opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	if got := specNames(specs); !reflect.DeepEqual(got, []string{"libx264"}) {
		t.Fatalf("candidates without ffmpeg h264_vaapi = %v, want [libx264]", got)
	}
}

func TestX11GrabOmitsVAAPIForNonH264Codec(t *testing.T) {
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-hevc",
		" V..... h264_vaapi \n V..... libx265 \n")
	withFakeVAAPIProbe(t, "/dev/dri/renderD128", true)

	opts := videoOptions{FPS: 60, Bitrate: "24M", Codec: "hevc", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(x11TestTarget(), opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	for _, name := range specNames(specs) {
		if name == "h264_vaapi" {
			t.Fatalf("h264_vaapi offered for an HEVC session: %v", specNames(specs))
		}
	}
}

func TestX11GrabSkipsVAAPIMarkedUnavailable(t *testing.T) {
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-vaapi-unhealthy",
		" V..... h264_vaapi \n V..... libx264 \n")
	withFakeVAAPIProbe(t, "/dev/dri/renderD128", true)
	markEncoderUnavailable(ffmpeg, "h264_vaapi")

	opts := videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(x11TestTarget(), opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	if got := specNames(specs); !reflect.DeepEqual(got, []string{"libx264"}) {
		t.Fatalf("candidates after a permanent VA-API failure = %v, want [libx264]", got)
	}
}

// --- VA-API capability probe --------------------------------------------

// scaleVAAPIHelpFixture mirrors `ffmpeg -h filter=scale_vaapi` on a build that
// has the filter, trimmed to the parts the probe inspects.
const scaleVAAPIHelpFixture = `Filter scale_vaapi
  Scale to/from VAAPI surfaces.
    Inputs:
       #0: default (video)
    Outputs:
       #0: default (video)
scale_vaapi AVOptions:
   w                 <string>     ..FV....... Output video width (default "iw")
   h                 <string>     ..FV....... Output video height (default "ih")
   format            <string>     ..FV....... Output video format (software format of hardware frames)
   out_range         <int>        ..FV....... Output colour range (from 0 to 2) (default 0)
     tv              1            ..FV....... Limited range

Exiting with exit code 0
`

// scaleVAAPIHelpNoOutRange is an older build: the filter exists but predates
// the out_range option the generated command line depends on.
const scaleVAAPIHelpNoOutRange = `Filter scale_vaapi
  Scale to/from VAAPI surfaces.
scale_vaapi AVOptions:
   w                 <string>     ..FV....... Output video width (default "iw")
   format            <string>     ..FV....... Output video format (software format of hardware frames)

Exiting with exit code 0
`

// scaleVAAPIHelpUnknown is what ffmpeg prints for a filter it does not have.
// It exits 0, so the process error alone cannot be used as the guard.
const scaleVAAPIHelpUnknown = `Unknown filter 'scale_vaapi'.

Exiting with exit code 0
`

func withFakeFilterHelp(t *testing.T, ffmpeg, filter, help string) {
	t.Helper()
	key := ffmpeg + "::" + filter
	filterHelpMu.Lock()
	filterHelpOutput[key] = help
	filterHelpMu.Unlock()
	t.Cleanup(func() {
		filterHelpMu.Lock()
		delete(filterHelpOutput, key)
		filterHelpMu.Unlock()
	})
}

func withFakeRenderNodes(t *testing.T, nodes ...string) {
	t.Helper()
	prev := vaapiRenderNodes
	vaapiRenderNodes = func() []string { return nodes }
	t.Cleanup(func() { vaapiRenderNodes = prev })
}

func TestVAAPIScaleFilterUsable(t *testing.T) {
	cases := []struct {
		name string
		help string
		want bool
	}{
		{name: "supported", help: scaleVAAPIHelpFixture, want: true},
		// ffmpeg exits 0 for an unknown filter, so the text is the only signal.
		{name: "unknown filter", help: scaleVAAPIHelpUnknown, want: false},
		{name: "missing out_range", help: scaleVAAPIHelpNoOutRange, want: false},
		{name: "empty", help: "", want: false},
		{name: "unrelated output", help: "ffmpeg version 8.0.1\n", want: false},
	}
	for _, tc := range cases {
		if got := vaapiScaleFilterUsable(tc.help); got != tc.want {
			t.Errorf("%s: vaapiScaleFilterUsable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProbeVAAPISupportAcceptsUsableFilter(t *testing.T) {
	ffmpeg := "ffmpeg-probe-ok"
	withFakeRenderNodes(t, "/dev/dri/renderD129", "/dev/dri/renderD128")
	withFakeFilterHelp(t, ffmpeg, vaapiScaleFilter, scaleVAAPIHelpFixture)

	device, ok := probeVAAPISupport(ffmpeg)
	if !ok {
		t.Fatal("probeVAAPISupport rejected a build that has scale_vaapi with out_range")
	}
	if device != "/dev/dri/renderD128" {
		t.Errorf("device = %q, want the lowest-numbered render node", device)
	}
}

func TestProbeVAAPISupportRejectsUnknownFilter(t *testing.T) {
	ffmpeg := "ffmpeg-probe-unknown-filter"
	withFakeRenderNodes(t, "/dev/dri/renderD128")
	withFakeFilterHelp(t, ffmpeg, vaapiScaleFilter, scaleVAAPIHelpUnknown)

	if device, ok := probeVAAPISupport(ffmpeg); ok {
		t.Fatalf("probeVAAPISupport accepted a build without scale_vaapi (device %q)", device)
	}
}

func TestProbeVAAPISupportRejectsFilterWithoutOutRange(t *testing.T) {
	ffmpeg := "ffmpeg-probe-old-filter"
	withFakeRenderNodes(t, "/dev/dri/renderD128")
	withFakeFilterHelp(t, ffmpeg, vaapiScaleFilter, scaleVAAPIHelpNoOutRange)

	if device, ok := probeVAAPISupport(ffmpeg); ok {
		t.Fatalf("probeVAAPISupport accepted scale_vaapi without out_range (device %q)", device)
	}
}

func TestProbeVAAPISupportWithoutRenderNode(t *testing.T) {
	ffmpeg := "ffmpeg-probe-no-node"
	withFakeRenderNodes(t)
	withFakeFilterHelp(t, ffmpeg, vaapiScaleFilter, scaleVAAPIHelpFixture)

	if _, ok := probeVAAPISupport(ffmpeg); ok {
		t.Fatal("probeVAAPISupport accepted a host with no DRM render node")
	}
}

func TestGlobVAAPIRenderNodesMissingDirectory(t *testing.T) {
	prev := vaapiRenderNodeGlob
	vaapiRenderNodeGlob = "./testdata-no-such-directory/renderD*"
	t.Cleanup(func() { vaapiRenderNodeGlob = prev })

	if nodes := globVAAPIRenderNodes(); len(nodes) != 0 {
		t.Fatalf("globVAAPIRenderNodes = %v, want none", nodes)
	}
}

func TestX11GrabOffersVAAPIThroughTheRealProbe(t *testing.T) {
	// Exercises the whole guard chain rather than stubbing linuxVAAPIProbe.
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-real-probe",
		" V..... h264_vaapi \n V..... libx264 \n")
	withFakeRenderNodes(t, "/dev/dri/renderD128")
	withFakeFilterHelp(t, ffmpeg, vaapiScaleFilter, scaleVAAPIHelpFixture)

	opts := videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(x11TestTarget(), opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	if got := specNames(specs); !reflect.DeepEqual(got, []string{"h264_vaapi", "libx264"}) {
		t.Fatalf("candidates = %v, want [h264_vaapi libx264]", got)
	}
}

func TestX11GrabFallsBackWhenFFmpegLacksScaleVAAPI(t *testing.T) {
	// The software fallback must survive an ffmpeg build that advertises
	// h264_vaapi but exits 0 with "Unknown filter" for scale_vaapi.
	ffmpeg := withFakeFFmpegEncoders(t, "ffmpeg-x11-no-scale-vaapi",
		" V..... h264_vaapi \n V..... libx264 \n")
	withFakeRenderNodes(t, "/dev/dri/renderD128")
	withFakeFilterHelp(t, ffmpeg, vaapiScaleFilter, scaleVAAPIHelpUnknown)

	opts := videoOptions{FPS: 60, Bitrate: "24M", Codec: "h264", FFmpeg: ffmpeg}
	specs, err := buildCapturePipelines(x11TestTarget(), opts)
	if err != nil {
		t.Fatalf("buildCapturePipelines: %v", err)
	}
	if got := specNames(specs); !reflect.DeepEqual(got, []string{"libx264"}) {
		t.Fatalf("candidates = %v, want [libx264]", got)
	}
}
