//go:build linux

package main

// Linux-only, opt-in pipeline telemetry.
//
// Linux hosts run a wider spread of capture/encode stacks (PipeWire+GStreamer,
// x11grab+ffmpeg, VA-API/NVENC/x264) than Windows, so throughput regressions
// are hard to attribute from the standard session summary alone. This file
// adds a counter set on the encoded-stream read path plus a single bounded
// summary line, gated behind DRAWSPLAY_PERF=1 so default output is unchanged.
//
// Counters are owned by the pipeline pump goroutine in video.go (the same
// goroutine drains chunks, parses Annex-B units and tears the session down),
// so they intentionally use plain integers: no locks or atomics are added to
// the hot path.

import (
	"log"
	"os"
	"strings"
	"time"
)

// linuxPerfEnv opts a run into pipeline telemetry logging.
const linuxPerfEnv = "DRAWSPLAY_PERF"

// linuxPerfEnabled reports whether telemetry lines may be emitted. Counting is
// always cheap and always on; only the log output is gated.
func linuxPerfEnabled() bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv(linuxPerfEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// linuxPipelineStats accumulates encoded-stream counters for one pipeline run.
type linuxPipelineStats struct {
	name    string
	start   time.Time
	bytes   int64
	units   int64
	dropped int64
}

// linuxPipelineSnapshot is a derived, immutable view of the counters.
type linuxPipelineSnapshot struct {
	Name           string
	Elapsed        time.Duration
	Bytes          int64
	Units          int64
	Dropped        int64
	BytesPerSecond float64
	UnitsPerSecond float64
}

func newLinuxPipelineStats(name string, start time.Time) *linuxPipelineStats {
	return &linuxPipelineStats{name: name, start: start}
}

// addBytes counts encoded bytes read from the pipeline.
func (s *linuxPipelineStats) addBytes(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.bytes += int64(n)
}

// addUnits counts complete Annex-B access units produced by the parser.
func (s *linuxPipelineStats) addUnits(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.units += int64(n)
}

// addDropped counts units the pipeline dropped before transmission (temporal
// adaptation shedding non-reference frames).
func (s *linuxPipelineStats) addDropped(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.dropped += int64(n)
}

// snapshotAt derives rates as of now. Elapsed is clamped at zero and rates are
// zero for a zero/negative window, so no division by zero can occur.
func (s *linuxPipelineStats) snapshotAt(now time.Time) linuxPipelineSnapshot {
	if s == nil {
		return linuxPipelineSnapshot{}
	}
	snap := linuxPipelineSnapshot{
		Name:    s.name,
		Bytes:   s.bytes,
		Units:   s.units,
		Dropped: s.dropped,
	}
	elapsed := now.Sub(s.start)
	if elapsed <= 0 {
		return snap
	}
	snap.Elapsed = elapsed
	seconds := elapsed.Seconds()
	snap.BytesPerSecond = float64(s.bytes) / seconds
	snap.UnitsPerSecond = float64(s.units) / seconds
	return snap
}

// recordLinuxPipelineStats emits one summary line when telemetry is enabled.
// It reports whether a line was written.
func recordLinuxPipelineStats(s *linuxPipelineStats, now time.Time, reason string) bool {
	if s == nil || !linuxPerfEnabled() {
		return false
	}
	snap := s.snapshotAt(now)
	log.Printf("Perf [%s] %s: %.1fs %.2f MB (%.2f Mb/s) units=%d (%.1f/s) dropped=%d",
		snap.Name, reason, snap.Elapsed.Seconds(),
		float64(snap.Bytes)/1e6, snap.BytesPerSecond*8/1e6,
		snap.Units, snap.UnitsPerSecond, snap.Dropped)
	return true
}

// finish emits the end-of-session summary (see pipelineTelemetry in video.go).
func (s *linuxPipelineStats) finish() {
	recordLinuxPipelineStats(s, time.Now(), "session-end")
}

func init() {
	startPipelineTelemetry = func(name string) pipelineTelemetry {
		return newLinuxPipelineStats(name, time.Now())
	}
}
