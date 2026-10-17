//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

const auWriterEnv = "DRAWSPLAY_TEST_AU_WRITER"

// auWriterGap separates the stand-in encoder's frames: less than the idle
// flush interval, so a frame held until the next one is still released by the
// next one's start code, yet long enough for that hold-back to stand out.
const auWriterGap = 60 * time.Millisecond

// auStampLen is the width of the write time the stand-in encoder stamps into
// each slice, as ASCII digits right after the slice's first payload byte.
const auStampLen = 19

// auPipelineTestStream is what the stand-in encoder writes: frames that fit
// one write, a frame of a full-buffer write plus a short one, and a frame of
// exactly two full-buffer writes.
func auPipelineTestStream() auTestStream {
	return newAUTestStream(
		auTestIDR(3000),
		auTestP(1500),
		auTestP(50000),
		[][]byte{auTestAUD, auTestSlice(true, false, true, 900)},
		auTestP(2*ffmpegAVIOBufferSize),
		auTestP(700),
		auTestP(1200),
	)
}

func auStampOffset(unit []byte) int {
	return annexBNALHeaderOffset(unit) + 2
}

// TestAccessUnitWriterProcess is not a real test: it is the stand-in encoder
// run by TestRunEncodePipelineSendsFramesWithTheirLastWrite. It writes the
// stream to stdout as ffmpeg does (one WriteFile per write), stamping each
// frame's write time.
func TestAccessUnitWriterProcess(t *testing.T) {
	if os.Getenv(auWriterEnv) != "1" {
		return
	}
	for _, au := range auPipelineTestStream().aus {
		frame := bytes.Join(au, nil)
		last := au[len(au)-1]
		stamp := len(frame) - len(last) + auStampOffset(last)
		copy(frame[stamp:], fmt.Sprintf("%0*d", auStampLen, time.Now().UnixNano()))
		for _, w := range ffmpegWrites(frame, ffmpegAVIOBufferSize) {
			if _, err := os.Stdout.Write(w); err != nil {
				os.Exit(3)
			}
		}
		time.Sleep(auWriterGap)
	}
	os.Exit(0)
}

// packetRecorder parses the framed video stream, timing each packet.
type packetRecorder struct {
	pending []byte
	packets []recordedPacket
}

type recordedPacket struct {
	kind    byte
	payload []byte
	at      time.Time
}

func (r *packetRecorder) Write(p []byte) (int, error) {
	r.pending = append(r.pending, p...)
	for len(r.pending) >= 5 {
		size := int(binary.BigEndian.Uint32(r.pending[1:5]))
		if len(r.pending) < 5+size {
			break
		}
		payload := append([]byte(nil), r.pending[5:5+size]...)
		r.packets = append(r.packets, recordedPacket{kind: r.pending[0], payload: payload, at: time.Now()})
		r.pending = r.pending[5+size:]
	}
	return len(p), nil
}

func (r *packetRecorder) nals() []recordedPacket {
	var nals []recordedPacket
	for _, p := range r.packets {
		if p.kind == videoPacketNAL {
			nals = append(nals, p)
		}
	}
	return nals
}

func runAUTestPipeline(t *testing.T, spec pipelineSpec, timeout time.Duration) (*packetRecorder, bool, error) {
	t.Helper()
	prevDisabled := auEarlyFlushDisabled.Load()
	auEarlyFlushDisabled.Store(false)
	t.Cleanup(func() { auEarlyFlushDisabled.Store(prevDisabled) })
	videoStatusMu.Lock()
	prevStatus := videoStatus
	videoStatusMu.Unlock()
	t.Cleanup(func() {
		videoStatusMu.Lock()
		videoStatus = prevStatus
		videoStatusMu.Unlock()
	})
	target := captureTarget{Kind: captureDDAGrab, Width: 1920, Height: 1080}
	opts := videoOptions{Codec: "h264", FPS: 60, Bitrate: "24M"}
	out := &packetRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	produced, err := runEncodePipeline(ctx, out, opts, spec, &sessionMonitor{}, newRateController(opts.Bitrate),
		newTemporalController(), false, newSessionCongestionGate(target))
	return out, produced, err
}

func TestWindowsPipeReportsWriteBoundaries(t *testing.T) {
	if preparePipeWriteBoundaries == nil {
		t.Fatal("no pipe boundary hook on windows")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	if got := preparePipeWriteBoundaries(pr, pipelineReadSize); got != windowsPipeFullBound {
		t.Fatalf("pipe bound = %d, want %d", got, windowsPipeFullBound)
	}
	if got := preparePipeWriteBoundaries(bytes.NewReader(nil), pipelineReadSize); got != 0 {
		t.Fatalf("non-pipe bound = %d, want 0", got)
	}
}

func TestWindowsFFmpegSpecsDeclareAccessUnitWrites(t *testing.T) {
	specs := withAccessUnitWrites([]pipelineSpec{{Name: "a"}, {Name: "b"}}, videoOptions{Codec: "h264"})
	for _, s := range specs {
		if s.AUWriteChunk != ffmpegAVIOBufferSize {
			t.Fatalf("%s AUWriteChunk = %d, want %d", s.Name, s.AUWriteChunk, ffmpegAVIOBufferSize)
		}
	}
	if hevc := withAccessUnitWrites([]pipelineSpec{{Name: "h"}}, videoOptions{Codec: "hevc"}); hevc[0].AUWriteChunk != 0 {
		t.Fatalf("HEVC spec declared the H.264 access-unit pattern")
	}
}

// TestRunEncodePipelineSendsFramesWithTheirLastWrite proves on a real Windows
// pipe that each frame leaves with its final write instead of waiting for the
// next frame's start code.
func TestRunEncodePipelineSendsFramesWithTheirLastWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a stand-in encoder process")
	}
	t.Setenv(auWriterEnv, "1")
	spec := pipelineSpec{
		Name:         "au-writer",
		BaseEncoder:  "au-writer",
		Cmd:          os.Args[0],
		Args:         []string{"-test.run=^TestAccessUnitWriterProcess$"},
		AUWriteChunk: ffmpegAVIOBufferSize,
	}
	out, produced, err := runAUTestPipeline(t, spec, 20*time.Second)
	if !produced || !errors.Is(err, io.EOF) {
		t.Fatalf("runEncodePipeline = %v, %v; want output, then EOF", produced, err)
	}
	nals := out.nals()
	s := auPipelineTestStream()
	want := s.nals()
	if len(nals) != len(want) {
		t.Fatalf("sent %d NAL units, want %d", len(nals), len(want))
	}
	for k := range s.aus {
		i := s.lastNAL(k)
		unit := nals[i].payload
		at := auStampOffset(unit)
		if at < 0 || len(unit) < at+auStampLen {
			t.Fatalf("frame %d: slice too short for its stamp", k)
		}
		written, err := strconv.ParseInt(string(unit[at:at+auStampLen]), 10, 64)
		if err != nil {
			t.Fatalf("frame %d: bad write stamp: %v", k, err)
		}
		copy(unit[at:], bytes.Repeat([]byte{0xA5}, auStampLen))
		heldBack := nals[i].at.Sub(time.Unix(0, written))
		frameSize := len(bytes.Join(s.aus[k], nil))
		t.Logf("frame %d (%d bytes): sent %v after its write", k, frameSize, heldBack.Round(10*time.Microsecond))
		switch {
		case frameSize%ffmpegAVIOBufferSize == 0:
			if heldBack < auWriterGap*3/4 {
				t.Errorf("frame %d (%d bytes, whole writes) sent after %v; its end is unprovable", k, frameSize, heldBack)
			}
		default:
			if heldBack > auWriterGap/2 {
				t.Errorf("frame %d (%d bytes) held back %v", k, frameSize, heldBack)
			}
		}
	}
	for i := range want {
		if !bytes.Equal(nals[i].payload, want[i]) {
			t.Fatalf("NAL unit %d differs from the stream (%d vs %d bytes)", i, len(nals[i].payload), len(want[i]))
		}
	}
}

// TestRunEncodePipelineEarlyEmissionWithRealFFmpeg runs a real ffmpeg H.264
// encode (frames above and below the 32 KiB AVIO buffer) through the read
// loop. Every frame must reach the client well before the next one is
// encoded, and the stream must never trip the misprediction check. Set
// DRAWSPLAY_TEST_FFMPEG to an ffmpeg.exe, or have ffmpeg on PATH.
func TestRunEncodePipelineEarlyEmissionWithRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("runs ffmpeg")
	}
	ffmpeg := os.Getenv("DRAWSPLAY_TEST_FFMPEG")
	if ffmpeg == "" {
		var err error
		if ffmpeg, err = exec.LookPath("ffmpeg"); err != nil {
			t.Skip("ffmpeg not found")
		}
	}
	const fps, frames = 20, 60
	spec := pipelineSpec{
		Name:        "ffmpeg-au",
		BaseEncoder: "libx264",
		Cmd:         ffmpeg,
		Args: []string{
			"-hide_banner", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=s=1280x720:r=%d", fps),
			"-frames:v", strconv.Itoa(frames),
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
			"-x264-params", "sliced-threads=0", "-bf", "0", "-g", "15",
			"-b:v", "12M", "-maxrate", "12M", "-bufsize", "1500k",
			"-bsf:v", "h264_metadata=aud=insert",
			"-flush_packets", "1", "-f", "h264", "pipe:1",
		},
		AUWriteChunk: ffmpegAVIOBufferSize,
	}
	out, produced, err := runAUTestPipeline(t, spec, 30*time.Second)
	if !produced || errors.Is(err, errAccessUnitResync) || !errors.Is(err, io.EOF) {
		t.Fatalf("runEncodePipeline = %v, %v; want output, then EOF", produced, err)
	}
	if auEarlyFlushDisabled.Load() {
		t.Fatal("early emission was disabled by a misprediction")
	}
	// Each frame opens with an access unit delimiter, which leaves as soon
	// as its frame is read. A slice sent with its frame follows the AUD at
	// once; a slice held for the next frame's start code trails it by about
	// a frame interval.
	interval := time.Second / fps
	var audAt time.Time
	var slices, early, big int
	for _, n := range out.nals() {
		h := annexBNALHeaderOffset(n.payload)
		if h < 0 {
			continue
		}
		switch typ := n.payload[h] & 0x1F; {
		case typ == 9:
			audAt = n.at
		case (typ == 1 || typ == 5) && !audAt.IsZero():
			slices++
			if n.at.Sub(audAt) < interval/2 {
				early++
			}
			if len(n.payload) >= ffmpegAVIOBufferSize {
				big++
			}
		}
	}
	if slices != frames {
		t.Fatalf("got %d slices, want %d", slices, frames)
	}
	t.Logf("%d of %d frames sent with their last write (%d slices of 32 KiB or more)", early, slices, big)
	// The final frame may only be flushed at EOF.
	if early < slices-1 {
		t.Fatalf("only %d of %d frames were sent with their last write", early, slices)
	}
}
