//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"
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

// auStampOffset locates the write-time stamp in a slice NAL unit.
func auStampOffset(unit []byte) int {
	return annexBNALHeaderOffset(unit) + 2
}

// TestAccessUnitWriterProcess is not a real test: it is the stand-in encoder
// run by TestRunEncodePipelineSendsFramesWithTheirLastWrite. It writes the
// stream to stdout as ffmpeg does, stamping each frame's write time.
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
			for len(w) > 0 {
				n, err := syscall.Write(1, w)
				if err == syscall.EINTR {
					continue
				}
				if err != nil {
					os.Exit(3)
				}
				w = w[n:]
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

func TestRunEncodePipelineSendsFramesWithTheirLastWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a stand-in encoder process")
	}
	if preparePipeWriteBoundaries == nil {
		t.Fatal("no pipe boundary hook on linux")
	}
	// The bound the pipeline's pipe will get, from a pipe prepared alike.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	bound := preparePipeWriteBoundaries(pr, pipelineReadSize)
	pr.Close()
	pw.Close()
	t.Setenv(auWriterEnv, "1")
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

	spec := pipelineSpec{
		Name:         "au-writer",
		BaseEncoder:  "au-writer",
		Cmd:          os.Args[0],
		Args:         []string{"-test.run=^TestAccessUnitWriterProcess$"},
		AUWriteChunk: ffmpegAVIOBufferSize,
	}
	target := captureTarget{Kind: captureX11Grab, Width: 1920, Height: 1080}
	opts := videoOptions{Codec: "h264", FPS: 60, Bitrate: "24M"}
	out := &packetRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	produced, err := runEncodePipeline(ctx, out, opts, spec, &sessionMonitor{}, newRateController(opts.Bitrate),
		newTemporalController(), false, newSessionCongestionGate(target))
	if !produced || !errors.Is(err, io.EOF) {
		t.Fatalf("runEncodePipeline = %v, %v; want output, then EOF", produced, err)
	}

	var nals []recordedPacket
	for _, p := range out.packets {
		if p.kind == videoPacketNAL {
			nals = append(nals, p)
		}
	}
	s := auPipelineTestStream()
	want := s.nals()
	if len(nals) != len(want) {
		t.Fatalf("sent %d NAL units, want %d", len(nals), len(want))
	}
	t.Logf("pipe bound %d bytes", bound)
	prevSize := 0
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
		// The previous frame may have ended in full reads, which count
		// against the bound.
		budget := bound - prevSize/pipelineReadSize*pipelineReadSize
		switch {
		case frameSize%ffmpegAVIOBufferSize == 0:
			if heldBack < auWriterGap*3/4 {
				t.Errorf("frame %d (%d bytes, whole writes) sent after %v; its end is unprovable", k, frameSize, heldBack)
			}
		case frameSize < pipelineReadSize && frameSize <= budget:
			if heldBack > auWriterGap/2 {
				t.Errorf("frame %d (%d bytes) held back %v", k, frameSize, heldBack)
			}
		}
		prevSize = frameSize
	}
	for i := range want {
		if !bytes.Equal(nals[i].payload, want[i]) {
			t.Fatalf("NAL unit %d differs from the stream (%d vs %d bytes)", i, len(nals[i].payload), len(want[i]))
		}
	}
}

func TestStartCapturePipelinePreparesOnlyDeclaredPipes(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	prevDisabled := auEarlyFlushDisabled.Load()
	auEarlyFlushDisabled.Store(false)
	t.Cleanup(func() { auEarlyFlushDisabled.Store(prevDisabled) })
	fresh, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defaultSize := pipeCapacity(t, fresh)
	fresh.Close()
	w.Close()
	page := os.Getpagesize()

	for _, declared := range []bool{true, false} {
		spec := pipelineSpec{Name: "pipe-prep", Cmd: os.Args[0], Args: []string{"-test.run=^$"}}
		if declared {
			spec.AUWriteChunk = ffmpegAVIOBufferSize
		}
		pipeline, err := startCapturePipeline(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		f, ok := pipeline.stdout.(*os.File)
		if !ok {
			pipeline.stop()
			t.Fatalf("stdout is a %T, want *os.File", pipeline.stdout)
		}
		size := pipeCapacity(t, f)
		pipeline.stop()
		switch {
		case declared && pipeline.fullPipeBound != pipeFullBound(size, page):
			t.Errorf("declared pattern: bound %d for a %d-byte pipe, want %d", pipeline.fullPipeBound, size, pipeFullBound(size, page))
		case declared && size < pipeBoundaryCapacity(page, pipelineReadSize):
			t.Logf("declared pattern: the pipe stayed at %d bytes", size)
		case !declared && (pipeline.fullPipeBound != 0 || size != defaultSize):
			t.Errorf("undeclared pattern: bound %d, pipe %d bytes; want 0, %d", pipeline.fullPipeBound, size, defaultSize)
		}
	}
}
