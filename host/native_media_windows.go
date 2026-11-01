//go:build windows

package main

// Windows native capture/encode helper (native/drawsplay-media-win).
//
// With DrawSplay's own IddCx monitor the helper reads each frame straight
// from the driver's shared swap-chain texture, converts it to NV12 and
// encodes it with the GPU's Media Foundation H.264 encoder without the frame
// ever leaving the GPU, and bounds frames in flight with the tablet's acks.
// It replaces Desktop Duplication + ffmpeg for that monitor; every ffmpeg
// candidate stays behind it as the fallback (old driver, Parsec VDD,
// mirroring, no hardware encoder, or any failure before the first frame).

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// mediahelper/ holds a README (tracked so go:embed always matches) and, in release builds, drawsplay-media.exe
// (staged by scripts/build-release.ps1).
//
//go:embed mediahelper
var embeddedMediaHelper embed.FS

const embeddedMediaHelperName = "mediahelper/" + nativeHelperName + ".exe"

var embeddedHelperOnce struct {
	sync.Once
	path string
}

// platformNativeHelperPath extracts the embedded helper (once per process)
// to a content-addressed file under the user's local app data and returns
// its path, or "" when this build embeds none.
func platformNativeHelperPath() string {
	embeddedHelperOnce.Do(func() {
		data, err := embeddedMediaHelper.ReadFile(embeddedMediaHelperName)
		if err != nil || len(data) == 0 {
			return
		}
		base, err := os.UserCacheDir()
		if err != nil {
			return
		}
		sum := sha256.Sum256(data)
		dir := filepath.Join(base, "DrawSplay", "media-helper")
		path := filepath.Join(dir, nativeHelperName+"-"+hex.EncodeToString(sum[:6])+".exe")
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
			embeddedHelperOnce.path = path
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("Native media helper: cannot create %s: %v", dir, err)
			return
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o755); err != nil {
			log.Printf("Native media helper: cannot extract: %v", err)
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			log.Printf("Native media helper: cannot extract: %v", err)
			return
		}
		embeddedHelperOnce.path = path
	})
	return embeddedHelperOnce.path
}

// windowsNativeHelperArgs builds the helper command line for a target.
func windowsNativeHelperArgs(target captureTarget, opts videoOptions) []string {
	args := []string{
		"--monitor-id", strconv.Itoa(target.IDDMonitorID),
		"--fps", strconv.Itoa(opts.FPS),
		"--bitrate-kbps", strconv.Itoa(parseBitrateKbps(opts.Bitrate)),
		"--max-inflight", strconv.Itoa(nativeMaxInflight),
	}
	if clientRequestsKeyframes() {
		args = append(args, "--keyframes", "on-demand")
	}
	if clientHasFeature(clientFeatureCursor) {
		// The tablet draws the cursor: take it out of the frames so cursor
		// motion costs no encode and never waits behind a frame.
		args = append(args, "--cursor", "overlay")
	}
	return args
}

// withWindowsNativeHelperCandidate puts the helper ahead of specs when the
// target is a DrawSplay driver monitor and the helper's probe found a
// hardware encoder; otherwise it returns specs unchanged. "auto" enables it:
// the helper only works with the frame-export driver and exits before
// producing anything otherwise, so the ffmpeg fallback gates it.
func withWindowsNativeHelperCandidate(specs []pipelineSpec, opts videoOptions, target captureTarget) []pipelineSpec {
	mode := normalizeNativeMediaMode(opts.NativeMedia)
	if mode == nativeMediaModeOff || target.IDDMonitorID <= 0 || target.Mirrored ||
		strings.EqualFold(opts.Codec, "hevc") || strings.EqualFold(opts.Codec, "h265") {
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
	spec := pipelineSpec{
		Name:               "native_mf",
		BaseEncoder:        "h264_mf",
		Cmd:                path,
		Args:               windowsNativeHelperArgs(target, opts),
		Framed:             true,
		Input:              nativeHelperInput,
		SignalBT709Limited: true,
	}
	log.Printf("Native media helper enabled (%s); ffmpeg candidates remain as fallback", res.Vendor)
	return append([]pipelineSpec{spec}, specs...)
}
