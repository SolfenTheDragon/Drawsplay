package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

type testRecoverableDisplay struct {
	recoverErr error
	calls      int
}

func (d *testRecoverableDisplay) Target() captureTarget           { return captureTarget{} }
func (d *testRecoverableDisplay) Refresh() (captureTarget, error) { return captureTarget{}, nil }
func (d *testRecoverableDisplay) Description() string             { return "test" }
func (d *testRecoverableDisplay) Close()                          {}
func (d *testRecoverableDisplay) Recover() error                  { d.calls++; return d.recoverErr }

type testNonRecoverableDisplay struct{}

func (d *testNonRecoverableDisplay) Target() captureTarget           { return captureTarget{} }
func (d *testNonRecoverableDisplay) Refresh() (captureTarget, error) { return captureTarget{}, nil }
func (d *testNonRecoverableDisplay) Description() string             { return "nonrecoverable" }
func (d *testNonRecoverableDisplay) Close()                          {}

// mustUnits asserts the exact NAL units and remainder produced by splitAnnexB.
func mustUnits(t *testing.T, buf []byte, wantUnits [][]byte, wantRest []byte) {
	t.Helper()
	units, rest := splitAnnexB(buf)
	if len(units) != len(wantUnits) {
		t.Fatalf("got %d units, want %d", len(units), len(wantUnits))
	}
	for i := range units {
		if !bytes.Equal(units[i], wantUnits[i]) {
			t.Errorf("unit %d = % x, want % x", i, units[i], wantUnits[i])
		}
	}
	if !bytes.Equal(rest, wantRest) {
		t.Errorf("rest = % x, want % x", rest, wantRest)
	}
}

func TestSplitAnnexBNoStartCode(t *testing.T) {
	buf := []byte{0xAA, 0xBB, 0xCC}
	mustUnits(t, buf, nil, buf)
}

func TestSplitAnnexBSingleIncompleteUnit(t *testing.T) {
	// One start code with payload but no following start code: nothing is
	// complete yet, everything stays in rest.
	buf := []byte{0, 0, 0, 1, 0x67, 0x42}
	mustUnits(t, buf, nil, buf)
}

func TestSplitAnnexBTwoUnits(t *testing.T) {
	// NAL payloads never end in 0x00 (RBSP stop bit), so no fold ambiguity.
	unit1 := []byte{0, 0, 0, 1, 0x67, 0x42, 0x80}
	unit2 := []byte{0, 0, 1, 0x68, 0xCE}
	buf := append(append([]byte{}, unit1...), unit2...)
	mustUnits(t, buf, [][]byte{unit1}, unit2)
}

func TestSplitAnnexBThreeUnitsMixedStartCodes(t *testing.T) {
	unit1 := []byte{0, 0, 1, 0x67, 0x42}
	unit2 := []byte{0, 0, 0, 1, 0x68, 0xCE}
	unit3 := []byte{0, 0, 1, 0x65, 0x88}
	buf := append(append(append([]byte{}, unit1...), unit2...), unit3...)
	mustUnits(t, buf, [][]byte{unit1, unit2}, unit3)
}

func TestSplitAnnexBAdjacentStartCodes(t *testing.T) {
	// Two back-to-back 3-byte start codes: the second must not steal the
	// last byte of the first (regression guard for the start-1 fold).
	unit1 := []byte{0, 0, 1}
	buf := []byte{0, 0, 1, 0, 0, 1, 0x41}
	mustUnits(t, buf, [][]byte{unit1}, []byte{0, 0, 1, 0x41})
}

func TestSplitAnnexBPayloadEndingInZero(t *testing.T) {
	// The trailing zero of unit1's payload belongs to unit2's 4-byte start
	// code per Annex-B convention (encoders emit 00 00 00 01).
	buf := []byte{0, 0, 1, 0x67, 0x00, 0x00, 0x00, 0x01, 0x68}
	mustUnits(t, buf, [][]byte{{0, 0, 1, 0x67}}, []byte{0x00, 0x00, 0x00, 0x01, 0x68})
}

func TestWriteVideoPacketFraming(t *testing.T) {
	var out bytes.Buffer
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if err := writeVideoPacket(&out, videoPacketNAL, payload); err != nil {
		t.Fatalf("writeVideoPacket: %v", err)
	}
	got := out.Bytes()
	if len(got) != 5+len(payload) {
		t.Fatalf("frame length = %d, want %d", len(got), 5+len(payload))
	}
	if got[0] != videoPacketNAL {
		t.Errorf("type byte = %d, want %d", got[0], videoPacketNAL)
	}
	if n := binary.BigEndian.Uint32(got[1:5]); n != uint32(len(payload)) {
		t.Errorf("length field = %d, want %d", n, len(payload))
	}
	if !bytes.Equal(got[5:], payload) {
		t.Errorf("payload = % x, want % x", got[5:], payload)
	}
}

func TestAuthorizeTransportVideoAcceptsValidToken(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() {
		_, _ = client.Write([]byte("{\"type\":\"auth\",\"authToken\":\"secret\"}\n"))
	}()
	if !authorizeTransportVideo(server, "secret") {
		t.Fatal("authorizeTransportVideo rejected valid token")
	}
}

func TestAuthorizeTransportVideoRejectsWrongToken(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() {
		_, _ = client.Write([]byte("{\"type\":\"auth\",\"authToken\":\"wrong\"}\n"))
	}()
	if authorizeTransportVideo(server, "secret") {
		t.Fatal("authorizeTransportVideo accepted wrong token")
	}
}

func TestAuthorizeTransportVideoRejectsOversizedLine(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() {
		line := fmt.Sprintf("{\"type\":\"auth\",\"authToken\":\"%s\"}\n", strings.Repeat("a", transportVideoAuthMaxBytes))
		_, _ = client.Write([]byte(line))
	}()
	if authorizeTransportVideo(server, "secret") {
		t.Fatal("authorizeTransportVideo accepted oversized auth line")
	}
}

func TestTransientCaptureErrorClassification(t *testing.T) {
	for _, message := range []string{
		"Failed duplicating output",
		"Failed to enumerate DXGI output 1",
		"AcquireNextFrame failed: 887a0026",
	} {
		if !isTransientCaptureError(errors.New(message)) {
			t.Errorf("did not classify %q as transient capture loss", message)
		}
	}
	if isTransientCaptureError(errors.New("unknown encoder h264_fake")) {
		t.Fatal("encoder configuration error classified as transient capture loss")
	}
}

func TestCaptureRecoveryBackoffIsBounded(t *testing.T) {
	var recovery captureRecoveryState
	want := []time.Duration{
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		2 * time.Second,
		2 * time.Second,
		2 * time.Second,
		2 * time.Second,
	}
	for i, wantDelay := range want {
		delay, remaining, ok := recovery.nextDelay()
		if !ok || delay != wantDelay {
			t.Fatalf("attempt %d = (%s, %v), want (%s, true)", i+1, delay, ok, wantDelay)
		}
		if remaining != len(want)-i-1 {
			t.Fatalf("attempt %d remaining = %d, want %d", i+1, remaining, len(want)-i-1)
		}
	}
	if _, _, ok := recovery.nextDelay(); ok {
		t.Fatal("recovery allowed more than the bounded attempt count")
	}
	recovery.reset()
	if delay, _, ok := recovery.nextDelay(); !ok || delay != captureRecoveryInitialDelay {
		t.Fatalf("reset recovery delay = %s, ok=%v", delay, ok)
	}
}

func TestActiveVideoRestartDebouncesStartupAndCoalesces(t *testing.T) {
	var state activeVideoSessionState
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_, cancel := context.WithCancel(context.Background())
	now := time.Unix(100, 0)
	state.set(cancel, server)

	if disposition, _, _ := state.requestRestart(false, now); disposition != videoRestartDeferred {
		t.Fatalf("startup restart disposition = %v, want deferred", disposition)
	}
	state.markReady(now)
	if disposition, _, _ := state.requestRestart(false, now.Add(videoReconnectDebounce-time.Millisecond)); disposition != videoRestartDeferred {
		t.Fatalf("debounced restart disposition = %v, want deferred", disposition)
	}
	// A healthy stream that has recently produced output should not be
	// preempted by reconnect chatter.
	state.markOutput(now.Add(videoReconnectDebounce))
	if disposition, _, _ := state.requestRestart(false, now.Add(videoReconnectDebounce+time.Second)); disposition != videoRestartDeferred {
		t.Fatalf("healthy-stream restart disposition = %v, want deferred", disposition)
	}
	disposition, gotCancel, gotConn := state.requestRestart(false, now.Add(videoReconnectDebounce+videoReconnectHandoffMinSilence+time.Millisecond))
	if disposition != videoRestartRequested || gotCancel == nil || gotConn != server {
		t.Fatalf("stable restart = (%v, %v, %v), want requested with active handles", disposition, gotCancel != nil, gotConn == server)
	}
	if disposition, _, _ := state.requestRestart(false, now.Add(videoReconnectDebounce+videoReconnectHandoffMinSilence+2*time.Second)); disposition != videoRestartAlreadyRequested {
		t.Fatalf("duplicate restart disposition = %v, want already requested", disposition)
	}
}

func TestAttemptDisplayRecoveryRecoversWhenSupported(t *testing.T) {
	display := &testRecoverableDisplay{}
	attempts := 0
	if !attemptDisplayRecovery(display, &attempts, errors.New("capture recovery exhausted")) {
		t.Fatal("expected display recovery to succeed")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if display.calls != 1 {
		t.Fatalf("recover calls = %d, want 1", display.calls)
	}
}

func TestAttemptDisplayRecoveryStopsAtLimit(t *testing.T) {
	display := &testRecoverableDisplay{}
	attempts := displayRecoveryMaxAttempts
	if attemptDisplayRecovery(display, &attempts, errors.New("capture recovery exhausted")) {
		t.Fatal("unexpected recovery after reaching attempt limit")
	}
	if display.calls != 0 {
		t.Fatalf("recover calls = %d, want 0", display.calls)
	}
}

func TestAttemptDisplayRecoverySkipsUnsupportedDisplays(t *testing.T) {
	display := &testNonRecoverableDisplay{}
	attempts := 0
	if attemptDisplayRecovery(display, &attempts, errors.New("capture recovery exhausted")) {
		t.Fatal("unexpected recovery for non-recoverable display")
	}
}

func TestDisplayModeFromTarget(t *testing.T) {
	cases := []struct {
		target captureTarget
		mode   string
		kind   string
	}{
		{target: captureTarget{Kind: captureDDAGrab}, mode: "extend", kind: "ddagrab"},
		{target: captureTarget{Kind: capturePipeWire}, mode: "extend", kind: "pipewire"},
		{target: captureTarget{Kind: captureX11Grab}, mode: "mirror", kind: "x11grab"},
		{target: captureTarget{Kind: captureKind("other")}, mode: "unknown", kind: "other"},
	}
	for _, tc := range cases {
		mode, kind := displayModeFromTarget(tc.target)
		if mode != tc.mode || kind != tc.kind {
			t.Fatalf("displayModeFromTarget(%q) = (%q,%q), want (%q,%q)", tc.target.Kind, mode, kind, tc.mode, tc.kind)
		}
	}
}

func TestPublishVideoStatusClearsMetadataWhenIdle(t *testing.T) {
	publishVideoDisplay("mirror", "x11grab", "mirror display")
	publishVideoStatus(true, "x264enc", "8M")
	if got := currentVideoStatus(); got.DisplayMode != "mirror" || got.CaptureKind != "x11grab" {
		t.Fatalf("status metadata not preserved while streaming: %+v", got)
	}
	publishVideoStatus(false, "", "")
	got := currentVideoStatus()
	if got.Streaming || got.DisplayMode != "" || got.CaptureKind != "" || got.Display != "" {
		t.Fatalf("idle status not cleared: %+v", got)
	}
}

func TestIsStartupPipelineFailure(t *testing.T) {
	if !isStartupPipelineFailure(true, time.Second) {
		t.Fatal("expected early produced+failed pipeline to be treated as startup failure")
	}
	if isStartupPipelineFailure(false, time.Second) {
		t.Fatal("did not expect non-producing pipeline to match startup-produced failure")
	}
	if isStartupPipelineFailure(true, pipelineStartupFailureWindow+time.Millisecond) {
		t.Fatal("did not expect long-running pipeline to be treated as startup failure")
	}
}

func TestH264NALType(t *testing.T) {
	if got := h264NALType([]byte{0, 0, 0, 1, 0x67, 0x42}); got != 7 {
		t.Fatalf("4-byte start code type = %d, want 7", got)
	}
	if got := h264NALType([]byte{0, 0, 1, 0x65, 0x88}); got != 5 {
		t.Fatalf("3-byte start code type = %d, want 5", got)
	}
	if got := h264NALType([]byte{0x65, 0x88}); got != -1 {
		t.Fatalf("non-annexb type = %d, want -1", got)
	}
}
