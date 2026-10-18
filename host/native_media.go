package main

// Optional native capture/encode helper (native/drawsplay-media).
//
// The helper is one more pipelineSpec candidate placed in front of the ffmpeg
// ones, so every failure (missing binary, failed probe, unsupported GPU, bad
// first frame) falls through to the existing ffmpeg pipelines. Its wire
// protocol is native/drawsplay-media/PROTOCOL.md.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// vaapiH264Encoder names the VA-API H.264 encoder; it is shared by the
// Linux ffmpeg specs and the native helper spec, so it lives here to keep
// non-Linux builds compiling.
const vaapiH264Encoder = "h264_vaapi"

const (
	nativeHelperName        = "drawsplay-media"
	nativeHelperEnv         = "DRAWSPLAY_MEDIA_HELPER"
	nativeHelperProbeLimit  = 6 * time.Second
	nativeFrameHeaderSize   = 16
	nativeFrameMaxPayload   = 8 << 20
	nativeFrameKindAU       = 1
	nativeFrameKindTile     = 2
	nativeFrameKindMode     = 3
	nativeMediaModeAuto     = "auto"
	nativeMediaModeOn       = "on"
	nativeMediaModeOff      = "off"
	clientFeatureTiles      = "tile1"
	clientFeatureFrameMeta  = "meta1"
	captureModeDamage       = "damage"
	nativeKeyframeFlag      = 1
	nativeFirstFrameNALMask = 1<<7 | 1<<8 | 1<<5
)

var errNativeBadFirstFrame = errors.New("native helper first frame is not a decodable keyframe")

// nativeFrameHeader is the 16-byte header preceding every helper frame.
type nativeFrameHeader struct {
	PayloadLen uint32
	Kind       uint8
	Flags      uint8
	InputSeq   uint32
	PipeUs     uint32
}

func parseNativeFrameHeader(b []byte) nativeFrameHeader {
	return nativeFrameHeader{
		PayloadLen: binary.LittleEndian.Uint32(b[0:4]),
		Kind:       b[4],
		Flags:      b[5],
		InputSeq:   binary.LittleEndian.Uint32(b[8:12]),
		PipeUs:     binary.LittleEndian.Uint32(b[12:16]),
	}
}

// readNativeFrame reads one frame, reusing buf for the payload when it fits.
func readNativeFrame(r io.Reader, buf []byte) (nativeFrameHeader, []byte, error) {
	var raw [nativeFrameHeaderSize]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return nativeFrameHeader{}, buf, err
	}
	h := parseNativeFrameHeader(raw[:])
	if h.PayloadLen > nativeFrameMaxPayload {
		return h, buf, fmt.Errorf("native frame payload %d bytes exceeds %d", h.PayloadLen, nativeFrameMaxPayload)
	}
	if cap(buf) < int(h.PayloadLen) {
		buf = make([]byte, h.PayloadLen)
	}
	buf = buf[:h.PayloadLen]
	if _, err := io.ReadFull(r, buf); err != nil {
		return h, buf, err
	}
	return h, buf, nil
}

// frameMetaPayload is the videoPacketFrameMeta body: big-endian inputSeq and
// helper pipeline time in microseconds.
func frameMetaPayload(inputSeq, pipeUs uint32) []byte {
	var b [8]byte
	binary.BigEndian.PutUint32(b[0:4], inputSeq)
	binary.BigEndian.PutUint32(b[4:8], pipeUs)
	return b[:]
}

// auIsDecodableStart reports whether an Annex-B access unit begins a
// decodable stream: SPS, PPS and an IDR slice.
func auIsDecodableStart(au []byte) bool {
	seen := 0
	for _, nal := range splitAnnexBAll(au) {
		if t := h264NALType(nal); t > 0 && t < 32 {
			seen |= 1 << t
		}
	}
	return seen&nativeFirstFrameNALMask == nativeFirstFrameNALMask
}

// splitAnnexBAll splits a complete buffer into NAL units, including the
// trailing one.
func splitAnnexBAll(b []byte) [][]byte {
	units, rest := splitAnnexB(b)
	if tail, ok := flushAnnexBTail(rest); ok {
		units = append(units, tail)
	}
	return units
}

// Client-advertised video-channel extensions.
var clientFeatures = struct {
	sync.RWMutex
	set map[string]bool
}{set: map[string]bool{}}

func publishClientVideoFeatures(features []string) {
	set := make(map[string]bool, len(features))
	for _, f := range features {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			set[f] = true
		}
	}
	clientFeatures.Lock()
	clientFeatures.set = set
	clientFeatures.Unlock()
}

func clientHasFeature(name string) bool {
	clientFeatures.RLock()
	defer clientFeatures.RUnlock()
	return clientFeatures.set[name]
}

// Input kicks: after each injected input event the active helper is told to
// capture now instead of waiting for the next damage notification.
var (
	latestInputSeq atomic.Uint32
	kickSignal     = make(chan struct{}, 1)
)

func noteInputInjected(seq uint32) {
	if seq == 0 {
		return
	}
	latestInputSeq.Store(seq)
	select {
	case kickSignal <- struct{}{}:
	default:
	}
}

// nativeHelperInput feeds the helper's stdin with "K <seq>" kicks until ctx
// ends.
func nativeHelperInput(ctx context.Context, w io.Writer) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-kickSignal:
			if _, err := fmt.Fprintf(w, "K %d\n", latestInputSeq.Load()); err != nil {
				return err
			}
		}
	}
}

type nativeProbeResult struct {
	OK         bool   `json:"ok"`
	Reason     string `json:"reason"`
	Vendor     string `json:"vendor"`
	Entrypoint string `json:"entrypoint"`
	Profile    string `json:"profile"`
	Version    int    `json:"helperVersion"`
}

var nativeProbeCache = struct {
	sync.Mutex
	byPath map[string]nativeProbeResult
}{byPath: map[string]nativeProbeResult{}}

// findNativeHelper locates the helper binary: the env override, then next to
// the host executable.
func findNativeHelper() string {
	if p := strings.TrimSpace(os.Getenv(nativeHelperEnv)); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
		log.Printf("Native media helper: %s=%q is not a file; ignoring", nativeHelperEnv, p)
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	name := nativeHelperName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

func probeNativeHelper(path string) nativeProbeResult {
	nativeProbeCache.Lock()
	defer nativeProbeCache.Unlock()
	if r, ok := nativeProbeCache.byPath[path]; ok {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), nativeHelperProbeLimit)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "--probe")
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	var res nativeProbeResult
	if err := cmd.Run(); err != nil {
		res.Reason = fmt.Sprintf("probe failed: %v %s", err, strings.TrimSpace(errOut.String()))
	} else if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &res); err != nil {
		res = nativeProbeResult{Reason: fmt.Sprintf("unparseable probe output: %v", err)}
	}
	nativeProbeCache.byPath[path] = res
	return res
}

// nativeVendorAllowed decides whether the helper may be preferred over
// ffmpeg for the probed VA driver. "on" trusts any driver that passed the
// real test encode; "auto" keeps to drivers verified on hardware (Intel iHD).
func nativeVendorAllowed(mode, vendor string) bool {
	switch mode {
	case nativeMediaModeOn:
		return true
	case nativeMediaModeOff:
		return false
	}
	return strings.Contains(strings.ToLower(vendor), "intel")
}

func normalizeNativeMediaMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case nativeMediaModeOn:
		return nativeMediaModeOn
	case nativeMediaModeOff:
		return nativeMediaModeOff
	}
	return nativeMediaModeAuto
}

// nativeHelperArgs builds the run command line for a capture rectangle.
func nativeHelperArgs(display string, rect displayRect, fps int, bitrate string, tiles bool) []string {
	args := []string{"--display", display}
	if !rect.empty() {
		args = append(args,
			"--x", strconv.Itoa(rect.X), "--y", strconv.Itoa(rect.Y),
			"--w", strconv.Itoa(rect.W&^1), "--h", strconv.Itoa(rect.H&^1))
	}
	args = append(args, "--fps", strconv.Itoa(fps), "--bitrate-kbps", strconv.Itoa(parseBitrateKbps(bitrate)))
	if tiles {
		args = append(args, "--tiles", "auto")
	} else {
		args = append(args, "--tiles", "off")
	}
	return args
}

// withNativeHelperCandidate puts the helper spec ahead of specs when it is
// usable; otherwise it returns specs unchanged.
func withNativeHelperCandidate(specs []pipelineSpec, opts videoOptions, target captureTarget, display string) []pipelineSpec {
	mode := normalizeNativeMediaMode(opts.NativeMedia)
	if mode == nativeMediaModeOff || strings.EqualFold(opts.Codec, "hevc") || strings.EqualFold(opts.Codec, "h265") {
		return specs
	}
	path := findNativeHelper()
	if path == "" {
		return specs
	}
	res := probeNativeHelper(path)
	if !res.OK {
		log.Printf("Native media helper unavailable (%s); using ffmpeg", res.Reason)
		return specs
	}
	if !nativeVendorAllowed(mode, res.Vendor) {
		log.Printf("Native media helper skipped for VA driver %q in %s mode; using ffmpeg (set nativeMediaHelper to \"on\" to try it)", res.Vendor, mode)
		return specs
	}
	spec := pipelineSpec{
		Name:        "native_vaapi",
		BaseEncoder: vaapiH264Encoder,
		Cmd:         path,
		Args:        nativeHelperArgs(display, target.Rect, opts.FPS, opts.Bitrate, clientHasFeature(clientFeatureTiles)),
		Framed:      true,
		Input:       nativeHelperInput,
	}
	log.Printf("Native media helper enabled (%s, %s); ffmpeg candidates remain as fallback", res.Vendor, res.Entrypoint)
	return append([]pipelineSpec{spec}, specs...)
}

// runFramedPipeline pumps a helper's framed stdout to the client. As with
// runEncodePipeline, an error before any output lets the caller fall back to
// the next candidate.
func runFramedPipeline(ctx context.Context, w io.Writer, opts videoOptions, spec pipelineSpec, monitor *sessionMonitor, rc *rateController, temporal *temporalController, expectBlackout bool, gate *congestionGate) (bool, error) {
	pipeline, err := startCapturePipeline(ctx, spec)
	if err != nil {
		return false, err
	}
	defer pipeline.stop()

	var produced atomic.Bool
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		startDeadline := time.Now().Add(encodeStartTimeout)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				pipeline.stop()
				return
			case <-ticker.C:
				if monitor.gone() || sessionDisplayBlackout() != expectBlackout ||
					(!produced.Load() && time.Now().After(startDeadline)) {
					pipeline.stop()
					return
				}
			}
		}
	}()

	encoderName := spec.BaseEncoder
	if encoderName == "" {
		encoderName = spec.Name
	}
	sendMeta := clientHasFeature(clientFeatureFrameMeta)
	sendTiles := clientHasFeature(clientFeatureTiles)
	nextSummary := time.Now().Add(sessionSummaryInterval)
	telemetry := newPipelineTelemetry(spec.Name)
	defer telemetry.finish()
	var sentNALs, sentBytes int64
	var frame []byte
	first := true
	for {
		hdr, payload, rerr := readNativeFrame(pipeline.stdout, frame)
		frame = payload
		if rerr != nil {
			if sessionDisplayBlackout() != expectBlackout {
				return produced.Load(), errPrivacyModeChanged
			}
			if ctx.Err() != nil {
				return produced.Load(), ctx.Err()
			}
			if !produced.Load() {
				return false, fmt.Errorf("native helper produced no output: %v (%s)", rerr, pipeline.errorTail())
			}
			return true, fmt.Errorf("native helper output: %w (%s)", rerr, pipeline.errorTail())
		}
		if sessionDisplayBlackout() != expectBlackout {
			return produced.Load(), errPrivacyModeChanged
		}
		if first {
			if hdr.Kind != nativeFrameKindAU || !auIsDecodableStart(payload) {
				return false, errNativeBadFirstFrame
			}
			first = false
		}
		markActiveVideoSessionOutput()
		if produced.CompareAndSwap(false, true) {
			markActiveVideoSessionReady()
			cfg, _ := json.Marshal(videoConfigMsg{
				Codec:       opts.Codec,
				HostCodecs:  hostVideoCodecs,
				Encoder:     encoderName,
				FPS:         opts.FPS,
				Bitrate:     opts.Bitrate,
				Pacing:      "latency",
				CaptureMode: captureModeDamage,
			})
			if err := writeVideoPacket(w, videoPacketConfig, cfg); err != nil {
				return true, fmt.Errorf("client write: %w", err)
			}
			publishVideoStatus(true, spec.Name, opts.Bitrate)
		}
		telemetry.addBytes(len(payload))
		switch hdr.Kind {
		case nativeFrameKindAU:
			if sendMeta {
				if err := writeVideoPacket(w, videoPacketFrameMeta, frameMetaPayload(hdr.InputSeq, hdr.PipeUs)); err != nil {
					return true, fmt.Errorf("client write: %w", err)
				}
			}
			units := splitAnnexBAll(payload)
			telemetry.addUnits(len(units))
			for _, unit := range units {
				if err := writeVideoPacket(w, videoPacketNAL, unit); err != nil {
					return true, fmt.Errorf("client write: %w", err)
				}
				sentNALs++
				sentBytes += int64(len(unit))
			}
		case nativeFrameKindTile, nativeFrameKindMode:
			if !sendTiles {
				break
			}
			ptype := byte(videoPacketTile)
			if hdr.Kind == nativeFrameKindMode {
				ptype = videoPacketMode
			}
			if err := writeVideoPacket(w, ptype, payload); err != nil {
				return true, fmt.Errorf("client write: %w", err)
			}
			sentBytes += int64(len(payload))
		}
		switch adaptToLinkFeedback(monitor, rc, temporal, gate, spec) {
		case congestionActionRestart:
			return true, errRateChanged
		}
		if now := time.Now(); now.After(nextSummary) {
			nextSummary = now.Add(sessionSummaryInterval)
			logSessionSummary(spec.Name, opts.Bitrate, sentNALs, sentBytes, monitor.stats(), monitor.takeStaleFrames())
		}
	}
}
