package main

// Capture pipeline specification shared by all platforms.
//
// A video session asks its platform for an ordered list of pipelineSpec
// candidates (pipeline_windows.go / pipeline_linux.go) and tries them until
// one produces output. Each spec is a complete external command line whose
// stdout is a raw H.264 Annex-B byte stream; process management lives in
// ffmpeg.go (startCapturePipeline), which is command-agnostic.

import (
	"fmt"
	"strconv"
	"strings"
)

// ffmpegAVIOBufferSize is libavformat's IO_BUFFER_SIZE, the write size of an
// ffmpeg pipe:1 output.
const ffmpegAVIOBufferSize = 32768

// pipelineSpec is one runnable capture/encode candidate.
type pipelineSpec struct {
	// Name identifies the encoder for logs and the client config packet
	// (e.g. "h264_nvenc", "x264enc").
	Name string
	// BaseEncoder is the canonical encoder family name reported to clients
	// even when Name includes an internal variant suffix.
	BaseEncoder string
	// SupportsTemporalDrop marks specs configured to emit droppable
	// enhancement frames (non-reference P-slices).
	SupportsTemporalDrop bool
	// Cmd is the executable to run (ffmpeg or gst-launch-1.0 path).
	Cmd string
	// Args is the full argument list. Stdout must be Annex-B H.264.
	Args []string
	// AUWriteChunk, when > 0, declares that the process writes nothing to
	// stdout but H.264 access units, each as write(2) calls of exactly
	// AUWriteChunk bytes followed by one shorter final call (ffmpeg's raw
	// muxer flushing its AVIO buffer under -flush_packets 1). It lets
	// runEncodePipeline emit a frame's last NAL as soon as that final write
	// has been read (see accessUnitTracker). Zero keeps the hold-back.
	AUWriteChunk int
}

// parseBitrateKbps converts an ffmpeg-style bitrate string ("8M", "2500k",
// "800000") into kilobits per second for tools that take integer kbps
// (GStreamer encoders). Falls back to 8000 kbps on unparseable input.
func parseBitrateKbps(bitrate string) int {
	s := strings.TrimSpace(strings.ToUpper(bitrate))
	mult := 1 // value already in kbps after suffix handling below
	switch {
	case strings.HasSuffix(s, "M"):
		s = strings.TrimSuffix(s, "M")
		mult = 1000
	case strings.HasSuffix(s, "K"):
		s = strings.TrimSuffix(s, "K")
	default:
		// Bare numbers are bits per second in ffmpeg syntax.
		if v, err := strconv.Atoi(s); err == nil {
			return v / 1000
		}
		return 8000
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 8000
	}
	return int(v * float64(mult))
}

// rateControlArgs is the common ffmpeg rate-control + output tail shared by
// every ffmpeg-based spec. The VBV buffer is capped at 125 ms of stream and
// packets are flushed to stdout as soon as they are muxed: a large VBV lets
// the encoder emit multi-frame bursts that queue up in transit, and the
// default 32 KB avio buffer can hold frames back for tens of milliseconds
// (hundreds after an adaptive-bitrate step-down).
//
// With -flush_packets 1 the raw muxer also gives stdout a fixed shape: each
// packet (one access unit) passes through the empty avio buffer and leaves as
// write(2) calls of ffmpegAVIOBufferSize bytes plus one shorter final call
// (see pipelineSpec.AUWriteChunk).
func rateControlArgs(opts videoOptions) []string {
	kbps := parseBitrateKbps(opts.Bitrate)
	vbvKbits := kbps / 8
	if opts.FPS > 0 {
		// ~1.25 frames worth of VBV to bound end-to-end latency spikes.
		vbvKbits = kbps * 5 / (4 * opts.FPS)
	}
	if vbvKbits < 100 {
		vbvKbits = 100
	}
	container := "h264"
	if strings.EqualFold(opts.Codec, "hevc") || strings.EqualFold(opts.Codec, "h265") {
		container = "hevc"
	}
	return []string{
		"-b:v", opts.Bitrate,
		"-maxrate", opts.Bitrate,
		"-bufsize", fmt.Sprintf("%dk", vbvKbits),
		"-flush_packets", "1",
		"-f", container,
		"pipe:1",
	}
}

// ffmpegSpecs builds one ffmpeg pipelineSpec per detected encoder, sharing
// the given capture input arguments. Used by both platforms' ffmpeg paths.
func ffmpegSpecs(opts videoOptions, captureArgs []string) ([]pipelineSpec, error) {
	encoders := detectEncoders(opts.FFmpeg, opts.Codec)
	if len(encoders) == 0 {
		return nil, fmt.Errorf("no usable %s encoder found in ffmpeg", opts.Codec)
	}
	specs := make([]pipelineSpec, 0, len(encoders))
	for _, enc := range encoders {
		specs = append(specs, buildFFmpegSpec(opts, captureArgs, enc, encoderArgs(enc, opts.FPS)))
	}
	return specs, nil
}

func buildFFmpegSpec(opts videoOptions, captureArgs []string, encoder string, encArgs []string) pipelineSpec {
	args := []string{"-hide_banner", "-loglevel", "warning"}
	args = append(args, captureArgs...)
	args = append(args, "-c:v", encoder)
	args = append(args, encArgs...)
	args = append(args, rateControlArgs(opts)...)
	return pipelineSpec{Name: encoder, BaseEncoder: encoder, Cmd: opts.FFmpeg, Args: args}
}
