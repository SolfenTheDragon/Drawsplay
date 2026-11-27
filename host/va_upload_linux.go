//go:build linux

package main

// vapostproc upload probe.
//
// The vah264enc candidate hands vapostproc the PipeWire stream's
// system-memory frames, which it uploads into video memory and converts to
// NV12 there. On some stacks that upload is pathologically slow when the
// frames live in memory vapostproc did not allocate (PipeWire's memfd
// buffers, or videotestsrc's own): measured on Intel Lunar Lake (xe kernel
// driver, iHD 26.4, GStreamer 1.24.2) it ran at ~4 fps at 1920x1200 and ~12
// fps at 640x400, while the same frames copied into vapostproc's own pool by
// a single-threaded videoconvert (BGRx -> BGRA, a plain byte copy) went
// through at ~52 fps for ~32 % of one core. The KDE Plasma session on that
// machine streamed at 3-4 fps because of it.
//
// The host cannot tell such a stack from the versions alone, so it measures
// once per process, in the background when the dependency preflight runs:
// 60 BGRx test frames through vapostproc with the copy and without it. Only
// when the copy works and the direct upload fails or takes over three times
// as long does the vah264enc candidate gain the copy; on every other stack
// the pipelines stay exactly as they were.

import (
	"context"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Test hooks.
var (
	vaUploadProbeFn = probeVAUploadSlow
	// vaUploadCopyNeeded reports whether the vah264enc candidate copies
	// frames into vapostproc's pool first.
	vaUploadCopyNeeded = vaUploadNeedsCopy
	vaUploadWait       = 6 * time.Second
)

var (
	vaUploadOnce   sync.Once
	vaUploadDone   = make(chan struct{})
	vaUploadResult bool
)

// startVAUploadProbe starts the probe once per process.
func startVAUploadProbe() {
	vaUploadOnce.Do(func() {
		go func() {
			vaUploadResult = vaUploadProbeFn()
			close(vaUploadDone)
		}()
	})
}

// vaUploadNeedsCopy is the probe's result, waiting for it briefly when a
// session starts before it is done. Without a result the pipelines stay as
// they are.
func vaUploadNeedsCopy() bool {
	startVAUploadProbe()
	select {
	case <-vaUploadDone:
		return vaUploadResult
	case <-time.After(vaUploadWait):
		return false
	}
}

const vaUploadProbeFrames = "60"

// vaUploadProbeArgs is the probe pipeline; copy puts the videoconvert copy in
// front of vapostproc.
func vaUploadProbeArgs(copy bool) []string {
	desc := "videotestsrc num-buffers=" + vaUploadProbeFrames + " pattern=ball ! video/x-raw,format=BGRx,width=1280,height=720,framerate=60/1 ! "
	if copy {
		desc += vaUploadCopy + " ! "
	}
	desc += "vapostproc ! video/x-raw(memory:VAMemory),format=NV12 ! fakesink sync=false"
	return append([]string{"-q"}, strings.Split(desc, " ")...)
}

// vaUploadCopy copies system-memory frames into the pool vapostproc offers.
// BGRA has BGRx's byte layout, so the copy converts nothing, and vapostproc
// ignores the alpha byte when it makes NV12.
const vaUploadCopy = "videoconvert n-threads=1 ! video/x-raw,format=BGRA"

func runVAUploadProbe(copy bool, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	err := exec.CommandContext(ctx, "gst-launch-1.0", vaUploadProbeArgs(copy)...).Run()
	return time.Since(start), err
}

// probeVAUploadSlow reports whether vapostproc's direct upload is broken
// enough here that the copy should go in front of it.
func probeVAUploadSlow() bool {
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		return false
	}
	if gstInspectElement("vapostproc") != nil {
		return false
	}
	viaCopy, err := runVAUploadProbe(true, 5*time.Second)
	if err != nil {
		// The copy does not help (or VA does not work at all): leave the
		// pipelines to the usual fallbacks.
		return false
	}
	limit := max(3*viaCopy, 2*time.Second)
	direct, err := runVAUploadProbe(false, limit)
	slow := vaUploadVerdict(viaCopy, direct, err)
	if slow {
		log.Printf("Capture: vapostproc uploads system-memory frames slowly here (%v for %s frames, %v with a copy into its own pool); the VA-API encoder copies frames first", direct.Round(time.Millisecond), vaUploadProbeFrames, viaCopy.Round(time.Millisecond))
	}
	return slow
}

// vaUploadVerdict decides from the two runs: the direct upload is slow when
// it failed (timed out or crashed) or took over three times as long as the
// copy.
func vaUploadVerdict(viaCopy, direct time.Duration, directErr error) bool {
	if directErr != nil {
		return true
	}
	return direct > 3*viaCopy
}
