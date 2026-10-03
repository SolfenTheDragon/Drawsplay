package main

// Capture/encode subprocess management and ffmpeg encoder knowledge.
//
// Why subprocesses instead of native encoder APIs: no cgo or vendor SDKs,
// identical behavior on Windows and Linux, and access to every hardware
// encoder the tool was built with. Native Media Foundation / VAAPI
// integrations can replace this behind the same startCapturePipeline
// interface later without touching the streaming code in video.go.
//
// What to run (capture source + encoder command line) is decided by the
// platform pipeline builders (pipeline_windows.go / pipeline_linux.go);
// this file only starts, supervises, and stops the chosen process.
//
// Encoder preference: NVENC > AMF > QSV > libx264 (software fallback). All
// profiles are tuned for lowest latency: no B-frames, zerolatency where the
// encoder supports it, 2-second keyframe interval for stream (re)join.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	pipelineStopWaitTimeout = 3 * time.Second
	pipelineKillWaitTimeout = 2 * time.Second
)

var (
	encoderProbeMu     sync.Mutex
	encoderProbeOutput = map[string]string{}
	filterHelpMu       sync.Mutex
	filterHelpOutput   = map[string]string{}
	encoderHealthMu    sync.Mutex
	encoderUnavailable = map[string]bool{}
	zeroCopyBlocked    = map[string]bool{}
)

// capturePipeline is a handle to a running capture/encode process.
type capturePipeline struct {
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	stdout   io.ReadCloser
	stderr   *boundedBuffer
	stopOnce sync.Once
}

// startCapturePipeline launches the spec's command and returns a handle
// exposing its Annex-B output stream.
func startCapturePipeline(ctx context.Context, spec pipelineSpec) (*capturePipeline, error) {
	log.Printf("Starting capture pipeline [%s]: %s %s", spec.Name, spec.Cmd, strings.Join(spec.Args, " "))

	cmdCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cmdCtx, spec.Cmd, spec.Args...)
	applyCaptureProcessPriority(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &boundedBuffer{max: 8 * 1024}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("%s start: %w", spec.Cmd, err)
	}
	applyCaptureProcessPriorityPostStart(cmd)
	return &capturePipeline{cmd: cmd, cancel: cancel, stdout: stdout, stderr: stderr}, nil
}

// stop terminates the process and reaps it. Safe to call from any
// goroutine, repeatedly (the session watcher and the deferred cleanup in
// runEncodePipeline can race).
func (p *capturePipeline) stop() {
	p.stopOnce.Do(func() {
		p.cancel()
		waitDone := make(chan error, 1)
		go func() {
			waitDone <- p.cmd.Wait()
		}()
		select {
		case <-waitDone:
			return
		case <-time.After(pipelineStopWaitTimeout):
		}

		if p.cmd.Process != nil {
			log.Printf("Capture pipeline did not stop within %s; killing process", pipelineStopWaitTimeout)
			_ = p.cmd.Process.Kill()
		}
		select {
		case <-waitDone:
		case <-time.After(pipelineKillWaitTimeout):
			log.Printf("Capture pipeline still not reaped after kill timeout (%s)", pipelineKillWaitTimeout)
		}
	})
}

// errorTail returns captured stderr for error reporting.
func (p *capturePipeline) errorTail() string {
	return strings.TrimSpace(p.stderr.String())
}

// detectedHostVideoCodecs reports codecs the host can currently encode.
// HEVC rollout is currently enabled on Windows ffmpeg paths only.
func detectedHostVideoCodecs(ffmpeg string) []string {
	codecs := []string{"h264"}
	if runtime.GOOS == "windows" && len(detectEncoders(ffmpeg, "hevc")) > 0 {
		codecs = append(codecs, "hevc")
	}
	return codecs
}

// detectEncoders returns usable encoder names for the negotiated codec in
// preference order by asking ffmpeg what it was built with.
func detectEncoders(ffmpeg string, codec string) []string {
	text, err := ffmpegEncodersOutput(ffmpeg)
	if err != nil {
		log.Printf("ffmpeg not available (%v); video streaming disabled", err)
		return nil
	}
	var all []string
	var result []string
	cands := []string{"h264_nvenc", "h264_amf", "h264_qsv", "libx264"}
	if strings.EqualFold(codec, "hevc") || strings.EqualFold(codec, "h265") {
		cands = []string{"hevc_nvenc", "hevc_amf", "hevc_qsv", "libx265"}
	}
	for _, enc := range cands {
		if strings.Contains(text, " "+enc+" ") {
			all = append(all, enc)
			if isEncoderUnavailable(ffmpeg, enc) {
				continue
			}
			result = append(result, enc)
		}
	}
	// Avoid a hard lockout if runtime-health classification was too strict:
	// when every candidate was filtered, retry all advertised encoders.
	if len(result) == 0 {
		return all
	}
	return result
}

func encoderHealthKey(ffmpeg, encoder string) string {
	return strings.TrimSpace(ffmpeg) + "::" + strings.TrimSpace(encoder)
}

func isEncoderUnavailable(ffmpeg, encoder string) bool {
	if strings.HasPrefix(encoder, "libx") {
		return false
	}
	encoderHealthMu.Lock()
	defer encoderHealthMu.Unlock()
	return encoderUnavailable[encoderHealthKey(ffmpeg, encoder)]
}

func markEncoderUnavailable(ffmpeg, encoder string) {
	if strings.HasPrefix(encoder, "libx") {
		return
	}
	encoderHealthMu.Lock()
	encoderUnavailable[encoderHealthKey(ffmpeg, encoder)] = true
	encoderHealthMu.Unlock()
}

func markEncoderHealthy(ffmpeg, encoder string) {
	key := encoderHealthKey(ffmpeg, encoder)
	encoderHealthMu.Lock()
	delete(encoderUnavailable, key)
	encoderHealthMu.Unlock()
}

func isZeroCopyBlocked(ffmpeg, encoder string) bool {
	encoderHealthMu.Lock()
	defer encoderHealthMu.Unlock()
	return zeroCopyBlocked[encoderHealthKey(ffmpeg, encoder)]
}

func markZeroCopyBlocked(ffmpeg, encoder string) {
	encoderHealthMu.Lock()
	zeroCopyBlocked[encoderHealthKey(ffmpeg, encoder)] = true
	encoderHealthMu.Unlock()
}

func isPermanentEncoderInitError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	markers := []string{
		"cannot load nvcuda.dll",
		"dll amfrt64.dll failed to open",
		"error initializing an internal mfx session",
		"error creating a mfx session",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isZeroCopyInitError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	markers := []string{
		"failed to created derived device context",
		"failed to configure output pad on parsed_hwmap",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func ffmpegEncodersOutput(ffmpeg string) (string, error) {
	encoderProbeMu.Lock()
	cached, ok := encoderProbeOutput[ffmpeg]
	encoderProbeMu.Unlock()
	if ok {
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders").Output()
	if err != nil {
		return "", err
	}
	text := string(out)
	encoderProbeMu.Lock()
	encoderProbeOutput[ffmpeg] = text
	encoderProbeMu.Unlock()
	return text, nil
}

func ffmpegFilterHelpOutput(ffmpeg string, filter string) (string, error) {
	key := ffmpeg + "::" + filter
	filterHelpMu.Lock()
	cached, ok := filterHelpOutput[key]
	filterHelpMu.Unlock()
	if ok {
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-h", "filter="+filter).CombinedOutput()
	if err != nil {
		return "", err
	}
	text := string(out)
	filterHelpMu.Lock()
	filterHelpOutput[key] = text
	filterHelpMu.Unlock()
	return text, nil
}

// encoderArgs returns per-encoder low-latency ffmpeg settings.
func encoderArgs(encoder string, fps int) []string {
	gopFrames := fps * 2
	if gopFrames <= 0 {
		gopFrames = 120
	}
	gop := fmt.Sprintf("%d", gopFrames)
	switch encoder {
	case "h264_nvenc":
		return []string{"-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-delay", "0", "-bf", "0", "-g", gop, "-pix_fmt", "yuv420p"}
	case "hevc_nvenc":
		return []string{"-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-delay", "0", "-bf", "0", "-g", gop, "-pix_fmt", "yuv420p"}
	case "h264_amf":
		return []string{"-usage", "ultralowlatency", "-bf", "0", "-g", gop, "-pix_fmt", "yuv420p"}
	case "hevc_amf":
		return []string{"-usage", "ultralowlatency", "-bf", "0", "-g", gop, "-pix_fmt", "yuv420p"}
	case "h264_qsv":
		// Depth 2 overlaps one upload with one encode. Depth 1 minimizes
		// latency but serializes the expensive D3D11 readback/QSV upload and
		// falls short of 60 fps at tablet resolutions; depth 2 recovers
		// throughput while adding at most one frame of pipeline latency.
		return []string{"-preset", "veryfast", "-async_depth", "2", "-bf", "0", "-g", gop, "-pix_fmt", "nv12"}
	case "hevc_qsv":
		return []string{"-preset", "veryfast", "-async_depth", "2", "-bf", "0", "-g", gop, "-pix_fmt", "nv12"}
	case "libx265":
		return []string{
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-x265-params", fmt.Sprintf("bframes=0:keyint=%s:min-keyint=1:repeat-headers=1", gop),
			"-pix_fmt", "yuv420p",
		}
	default: // libx264
		return []string{
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			// zerolatency enables sliced threads (several slice NALs per
			// frame); the tablet feeds one NAL per decoder buffer and
			// renders nothing when frames arrive fragmented.
			"-x264-params", "sliced-threads=0",
			"-bf", "0", "-g", gop, "-pix_fmt", "yuv420p",
		}
	}
}

// boundedBuffer keeps only the first max bytes written — enough to surface
// ffmpeg's startup errors without unbounded growth on a chatty process.
type boundedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < b.max {
		remain := b.max - b.buf.Len()
		if len(p) > remain {
			b.buf.Write(p[:remain])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }
