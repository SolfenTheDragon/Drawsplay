package main

// Early emission of each frame's last NAL unit.
//
// splitAnnexB can only call a NAL unit complete once the next start code has
// arrived, and the next start code belongs to the next frame. So every
// frame's final NAL unit (the slice, i.e. nearly all of the frame) used to
// wait on the host for a whole frame period, 16.7 ms at 60 fps, before being
// sent. accessUnitTracker spots the end of an access unit as it is read, from
// two facts about pipelines that opt in via pipelineSpec.AUWriteChunk:
//
//  1. Write pattern. With -flush_packets 1, ffmpeg's raw muxer passes each
//     packet (one access unit) through its emptied AVIO buffer, so the unit
//     reaches stdout as write(2) calls of exactly AUWriteChunk bytes followed
//     by one shorter call. A write boundary E inside an access unit that
//     started at stream offset A therefore has (E-A) % AUWriteChunk == 0,
//     unless E is where that unit ends.
//
//  2. Pipe semantics. Some reads provably end on a write boundary; the
//     platform prepares the pipe and bounds what it holds when its writer
//     stops mid-write (preparePipeWriteBoundaries), and boundaryReads says
//     which reads those are.
//
// Access-unit starts come from the H.264 NAL unit sequence (7.4.1.2.3). When
// a boundary read ends an access unit, the trailing NAL unit is emitted at
// once. Correctness never rests on the prediction alone: after an early
// emission the next bytes must open with a start code, which no truncated
// NAL unit can be followed by (emulation prevention keeps 00 00 0x, x<=2, out
// of NAL payloads). A misprediction is therefore caught on the next read and
// surfaces as errAccessUnitResync.

import (
	"errors"
	"io"
	"sync/atomic"
)

// errAccessUnitResync reports that an early-emitted NAL unit continued in the
// stream. The pipeline restarts (the client may hold a truncated frame until
// the fresh IDR) and early emission stays off for the rest of the process.
var errAccessUnitResync = errors.New("access-unit boundary mispredicted")

// auEarlyFlushDisabled turns early emission off process-wide after a
// misprediction, so a writer that breaks the pattern costs one restart rather
// than one per session.
var auEarlyFlushDisabled atomic.Bool

// preparePipeWriteBoundaries is installed by platforms whose pipes let reads
// prove write boundaries (Linux, see pipe_linux.go): a read takes everything
// buffered, up to its size, in one step, and a writer stops partway through a
// write only on a full pipe. It is given a fresh pipe before any writer
// starts, may enlarge it, and returns B: the writer only ever stops mid-write
// while the pipe holds more than B bytes. 0 when no such bound is known.
var preparePipeWriteBoundaries func(stdout io.Reader, readSize int) int

// fullPipeBoundFor prepares a new pipeline's stdout for early emission when
// the spec declares the write pattern, returning the pipe's bound B.
func fullPipeBoundFor(spec pipelineSpec, stdout io.Reader) int {
	if spec.AUWriteChunk <= 0 || preparePipeWriteBoundaries == nil || auEarlyFlushDisabled.Load() {
		return 0
	}
	return preparePipeWriteBoundaries(stdout, pipelineReadSize)
}

// accessUnitTrackerFor enables early emission for a pipeline run when the spec
// declares the write pattern and its stdout carries boundary proofs.
func accessUnitTrackerFor(spec pipelineSpec, fullPipeBound int) *accessUnitTracker {
	if spec.AUWriteChunk <= 0 || fullPipeBound <= 0 || auEarlyFlushDisabled.Load() {
		return nil
	}
	return newAccessUnitTracker(spec.AUWriteChunk)
}

// boundaryReads tells which successive reads of a prepared pipe provably ended
// where a write ended. A read shorter than its buffer drained the pipe. A read
// r that ended mid-write came after its writer stopped mid-write, on a pipe
// holding more than fullPipe bytes, and nothing was written since. Each read
// in between took a full buffer (a short one would have drained the pipe and
// left r nothing), so all came after the last short read before r. A short r
// therefore returned more than fullPipe minus the bytes read since then.
type boundaryReads struct {
	fullPipe int
	// sinceDrain counts the bytes read since a read last drained the pipe
	// (a fresh pipe starts empty), capped at fullPipe.
	sinceDrain int
}

// next reports whether a read of n > 0 bytes into a buffer of bufSize bytes
// ended where a write ended.
func (b *boundaryReads) next(n, bufSize int) bool {
	if n >= bufSize {
		b.sinceDrain = min(b.sinceDrain+n, b.fullPipe)
		return false
	}
	proven := n <= b.fullPipe-b.sinceDrain
	b.sinceDrain = 0
	return proven
}

// accessUnitTracker follows access-unit boundaries in an H.264 Annex-B stream.
// Offsets count every byte the pipeline wrote, from 0.
type accessUnitTracker struct {
	writeChunk int64
	// end is the offset just past the last byte received.
	end int64
	// auStart is the offset of the current access unit's first byte, -1
	// while unknown. The first write of the stream starts the first unit.
	auStart int64
	// observed is the offset of the last NAL unit whose header was taken
	// into account, so the unit carried over as rest is not counted twice.
	observed int64
	// sawVCL reports that the current access unit already holds a slice,
	// i.e. that the next unit-opening NAL unit starts a new access unit.
	sawVCL bool
	// verify is set after an early emission: the next bytes must open with
	// a start code.
	verify bool
}

func newAccessUnitTracker(writeChunk int) *accessUnitTracker {
	return &accessUnitTracker{writeChunk: int64(writeChunk), observed: -1}
}

// split is splitAnnexB for a buffer whose last added bytes were just read;
// boundary reports that the read provably ended at a write boundary. When
// that boundary ends an access unit, the trailing NAL unit is returned as the
// last of units and rest is nil.
func (t *accessUnitTracker) split(buf []byte, added int, boundary bool) (units [][]byte, rest []byte, err error) {
	t.end += int64(added)
	if t.verify {
		switch leadingStartCode(buf) {
		case startCodeIncomplete:
			return nil, buf, nil
		case startCodeAbsent:
			return nil, nil, errAccessUnitResync
		}
		t.verify = false
	}
	units, rest = splitAnnexB(buf)
	offset := t.end - int64(len(rest))
	for _, unit := range units {
		offset -= int64(len(unit))
	}
	for _, unit := range units {
		t.observe(unit, offset)
		offset += int64(len(unit))
	}
	restSeen := t.observe(rest, offset)
	if !boundary || !restSeen || t.auStart < 0 || (t.end-t.auStart)%t.writeChunk == 0 {
		return units, rest, nil
	}
	// The write that ended at t.end was shorter than writeChunk: the last
	// write of an access unit, whose final NAL unit is rest.
	units = append(units, rest)
	t.auStart = t.end
	t.sawVCL = false
	t.verify = true
	return units, nil, nil
}

// forget drops access-unit tracking after the caller emitted buffered bytes on
// its own (the idle tail flush), which may have cut a unit short. Tracking
// resumes at the next access-unit start the NAL sequence reveals.
func (t *accessUnitTracker) forget() {
	t.auStart = -1
	t.sawVCL = false
	t.verify = false
}

// observe takes the NAL unit starting at offset off into account and reports
// whether its header has been seen (now or on an earlier call).
func (t *accessUnitTracker) observe(unit []byte, off int64) bool {
	if off <= t.observed {
		return off == t.observed
	}
	h := annexBNALHeaderOffset(unit)
	if h < 0 {
		return false
	}
	nalType := unit[h] & 0x1F
	vcl := nalType >= 1 && nalType <= 5
	if vcl && len(unit) < h+2 {
		return false // first_mb_in_slice has not arrived yet
	}
	t.observed = off
	if t.sawVCL && opensAccessUnit(nalType, unit[h+1:]) {
		t.auStart = off
		t.sawVCL = false
	}
	if vcl {
		t.sawVCL = true
	}
	return true
}

// opensAccessUnit reports whether a NAL unit of nalType that follows a slice of
// the current picture starts a new access unit (H.264 7.4.1.2.3): SEI, SPS,
// PPS, access unit delimiter, types 14-18, or the first slice of the next
// picture. A slice is first when first_mb_in_slice is 0, whose ue(v) code is a
// single 1 bit at the start of the payload.
func opensAccessUnit(nalType byte, payload []byte) bool {
	switch {
	case nalType >= 6 && nalType <= 9, nalType >= 14 && nalType <= 18:
		return true
	case nalType == 1 || nalType == 5:
		return len(payload) > 0 && payload[0]&0x80 != 0
	}
	return false
}

type startCodeCheck int

const (
	startCodePresent startCodeCheck = iota
	startCodeIncomplete
	startCodeAbsent
)

// leadingStartCode classifies how buf opens: with a start code (two or more
// zero bytes, then 0x01), possibly with one (zero bytes only, so far), or not.
func leadingStartCode(buf []byte) startCodeCheck {
	zeros := 0
	for zeros < len(buf) && buf[zeros] == 0 {
		zeros++
	}
	switch {
	case zeros == len(buf):
		return startCodeIncomplete
	case zeros >= 2 && buf[zeros] == 1:
		return startCodePresent
	default:
		return startCodeAbsent
	}
}
