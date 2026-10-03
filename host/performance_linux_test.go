//go:build linux

package main

import (
	"bytes"
	"log"
	"math"
	"strings"
	"testing"
	"time"
)

func captureLinuxPipelineStatsLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	fn()
	return buf.String()
}

func TestLinuxPipelineStatsDisabledByDefault(t *testing.T) {
	t.Setenv(linuxPerfEnv, "")
	start := time.Unix(1700000000, 0)
	stats := newLinuxPipelineStats("x264enc", start)
	stats.addBytes(4096)
	stats.addUnits(8)

	var emitted bool
	out := captureLinuxPipelineStatsLog(t, func() {
		emitted = recordLinuxPipelineStats(stats, start.Add(time.Second), "session-end")
	})
	if emitted {
		t.Errorf("recordLinuxPipelineStats emitted with %s unset, want suppressed", linuxPerfEnv)
	}
	if out != "" {
		t.Errorf("unexpected log output with telemetry disabled: %q", out)
	}

	// Counting must keep working even while logging is disabled.
	snap := stats.snapshotAt(start.Add(time.Second))
	if snap.Bytes != 4096 || snap.Units != 8 {
		t.Errorf("snapshot = %+v, want bytes=4096 units=8", snap)
	}
}

func TestLinuxPipelineStatsSnapshotRates(t *testing.T) {
	start := time.Unix(1700000000, 0)
	stats := newLinuxPipelineStats("vah264enc", start)
	stats.addBytes(1_000_000)
	stats.addBytes(500_000)
	stats.addUnits(90)
	stats.addUnits(30)
	stats.addDropped(3)

	snap := stats.snapshotAt(start.Add(2 * time.Second))
	if snap.Name != "vah264enc" {
		t.Errorf("snapshot.Name = %q, want vah264enc", snap.Name)
	}
	if snap.Elapsed != 2*time.Second {
		t.Errorf("snapshot.Elapsed = %v, want 2s", snap.Elapsed)
	}
	if snap.Bytes != 1_500_000 {
		t.Errorf("snapshot.Bytes = %d, want 1500000", snap.Bytes)
	}
	if snap.Units != 120 {
		t.Errorf("snapshot.Units = %d, want 120", snap.Units)
	}
	if snap.Dropped != 3 {
		t.Errorf("snapshot.Dropped = %d, want 3", snap.Dropped)
	}
	if snap.BytesPerSecond != 750_000 {
		t.Errorf("snapshot.BytesPerSecond = %v, want 750000", snap.BytesPerSecond)
	}
	if snap.UnitsPerSecond != 60 {
		t.Errorf("snapshot.UnitsPerSecond = %v, want 60", snap.UnitsPerSecond)
	}
}

func TestLinuxPipelineStatsZeroElapsed(t *testing.T) {
	start := time.Unix(1700000000, 0)
	stats := newLinuxPipelineStats("nvh264enc", start)
	stats.addBytes(1234)
	stats.addUnits(2)

	for _, now := range []time.Time{start, start.Add(-time.Second)} {
		snap := stats.snapshotAt(now)
		if snap.Elapsed != 0 {
			t.Errorf("snapshot(%v).Elapsed = %v, want 0", now, snap.Elapsed)
		}
		if math.IsNaN(snap.BytesPerSecond) || math.IsInf(snap.BytesPerSecond, 0) || snap.BytesPerSecond != 0 {
			t.Errorf("snapshot(%v).BytesPerSecond = %v, want 0", now, snap.BytesPerSecond)
		}
		if math.IsNaN(snap.UnitsPerSecond) || math.IsInf(snap.UnitsPerSecond, 0) || snap.UnitsPerSecond != 0 {
			t.Errorf("snapshot(%v).UnitsPerSecond = %v, want 0", now, snap.UnitsPerSecond)
		}
		if snap.Bytes != 1234 || snap.Units != 2 {
			t.Errorf("snapshot(%v) counters = %d bytes / %d units, want 1234 / 2", now, snap.Bytes, snap.Units)
		}
	}
}

func TestLinuxPipelineStatsEnabledEmitsSummary(t *testing.T) {
	t.Setenv(linuxPerfEnv, "1")
	start := time.Unix(1700000000, 0)
	stats := newLinuxPipelineStats("x264enc", start)
	stats.addBytes(2_000_000)
	stats.addUnits(240)
	stats.addDropped(4)

	var emitted bool
	out := captureLinuxPipelineStatsLog(t, func() {
		emitted = recordLinuxPipelineStats(stats, start.Add(4*time.Second), "session-end")
	})
	if !emitted {
		t.Fatalf("recordLinuxPipelineStats did not emit with %s=1", linuxPerfEnv)
	}
	for _, want := range []string{"x264enc", "session-end", "240", "4"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q missing %q", out, want)
		}
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("want exactly one summary line, got %q", out)
	}
}

func TestLinuxPipelineStatsNilSafe(t *testing.T) {
	t.Setenv(linuxPerfEnv, "1")
	var stats *linuxPipelineStats
	out := captureLinuxPipelineStatsLog(t, func() {
		stats.addBytes(10)
		stats.addUnits(1)
		stats.addDropped(1)
		if recordLinuxPipelineStats(stats, time.Unix(1700000000, 0), "session-end") {
			t.Errorf("recordLinuxPipelineStats emitted for nil stats")
		}
	})
	if out != "" {
		t.Errorf("unexpected log output for nil stats: %q", out)
	}
}

func TestLinuxPipelineTelemetryHookInstalled(t *testing.T) {
	if startPipelineTelemetry == nil {
		t.Fatal("startPipelineTelemetry hook not installed on linux")
	}
	telemetry := startPipelineTelemetry("x264enc")
	if telemetry == nil {
		t.Fatal("startPipelineTelemetry returned nil telemetry")
	}
	stats, ok := telemetry.(*linuxPipelineStats)
	if !ok {
		t.Fatalf("telemetry type = %T, want *linuxPipelineStats", telemetry)
	}
	telemetry.addBytes(64)
	telemetry.addUnits(2)
	telemetry.addDropped(1)
	snap := stats.snapshotAt(stats.start)
	if snap.Bytes != 64 || snap.Units != 2 || snap.Dropped != 1 {
		t.Errorf("snapshot = %+v, want bytes=64 units=2 dropped=1", snap)
	}
}
