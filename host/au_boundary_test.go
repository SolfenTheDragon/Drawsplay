package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// Synthetic H.264 NAL units. Payloads never contain 00 00 0x or end in 00, as
// in a real stream.
var (
	auTestSPS = []byte{0, 0, 0, 1, 0x67, 0x42, 0xC0, 0x1F, 0xDA, 0x80}
	auTestPPS = []byte{0, 0, 0, 1, 0x68, 0xCE, 0x3C, 0x80}
	auTestSEI = []byte{0, 0, 1, 0x06, 0x05, 0x04, 0xAB, 0xCD, 0xEF, 0x12, 0x80}
	auTestAUD = []byte{0, 0, 0, 1, 0x09, 0xF0}
)

// auTestBoundaryLimit is the boundary-proof read size of a default Linux pipe.
const auTestBoundaryLimit = 28672

// auTestSlice returns a slice NAL unit of size bytes: IDR or P, and the first
// slice of its picture (first_mb_in_slice 0) or a later one.
func auTestSlice(long, idr, first bool, size int) []byte {
	unit := []byte{0, 0, 1}
	if long {
		unit = []byte{0, 0, 0, 1}
	}
	header, lead := byte(0x41), byte(0x40) // first_mb_in_slice 1
	if idr {
		header = 0x65
	}
	if first {
		lead = 0x88
	}
	unit = append(unit, header, lead)
	for len(unit) < size {
		unit = append(unit, 0xA5)
	}
	return unit
}

// auTestIDR is a keyframe access unit of exactly size bytes.
func auTestIDR(size int) [][]byte {
	return [][]byte{auTestSPS, auTestPPS, auTestSlice(true, true, true, size-len(auTestSPS)-len(auTestPPS))}
}

// auTestP is a predicted access unit of exactly size bytes.
func auTestP(size int) [][]byte {
	return [][]byte{auTestSlice(true, false, true, size)}
}

type auTestStream struct {
	aus    [][][]byte
	data   []byte
	writes [][]byte
}

// newAUTestStream lays access units out as ffmpeg's raw muxer writes them.
func newAUTestStream(aus ...[][]byte) auTestStream {
	s := auTestStream{aus: aus}
	for _, au := range aus {
		joined := bytes.Join(au, nil)
		s.data = append(s.data, joined...)
		s.writes = append(s.writes, ffmpegWrites(joined, ffmpegAVIOBufferSize)...)
	}
	return s
}

// ffmpegWrites splits a packet into the write(2) calls of an AVIO context
// that is flushed after every packet.
func ffmpegWrites(packet []byte, chunk int) [][]byte {
	var writes [][]byte
	for len(packet) > 0 {
		n := min(chunk, len(packet))
		writes = append(writes, packet[:n])
		packet = packet[n:]
	}
	return writes
}

func (s auTestStream) nals() [][]byte {
	var all [][]byte
	for _, au := range s.aus {
		all = append(all, au...)
	}
	return all
}

// lastNAL is the stream-wide index of access unit k's last NAL unit.
func (s auTestStream) lastNAL(k int) int {
	n := 0
	for _, au := range s.aus[:k+1] {
		n += len(au)
	}
	return n - 1
}

type auTestRead struct {
	n        int
	boundary bool
}

// readsPerWrite reads every write on its own, with boundary proof for reads of
// at most limit bytes.
func readsPerWrite(writes [][]byte, limit int) []auTestRead {
	reads := make([]auTestRead, len(writes))
	for i, w := range writes {
		reads[i] = auTestRead{n: len(w), boundary: len(w) <= limit}
	}
	return reads
}

// auTestFeed drives a tracker the way runEncodePipeline does. It returns the
// emitted units with the index of the read that released each, and what is
// still buffered.
type auTestFeed struct {
	t       *testing.T
	tracker *accessUnitTracker
	data    []byte
	off     int
	buf     []byte
	units   [][]byte
	at      []int
	reads   int
}

func newAUTestFeed(t *testing.T, data []byte) *auTestFeed {
	return &auTestFeed{t: t, tracker: newAccessUnitTracker(ffmpegAVIOBufferSize), data: data}
}

func (f *auTestFeed) read(n int, boundary bool) error {
	chunk := f.data[f.off : f.off+n]
	f.off += n
	f.buf = append(f.buf, chunk...)
	units, rest, err := f.tracker.split(f.buf, len(chunk), boundary)
	if err != nil {
		return err
	}
	for _, unit := range units {
		f.units = append(f.units, append([]byte(nil), unit...))
		f.at = append(f.at, f.reads)
	}
	f.buf = append(f.buf[:0], rest...)
	f.reads++
	return nil
}

func (f *auTestFeed) feed(reads []auTestRead) {
	f.t.Helper()
	for _, r := range reads {
		if err := f.read(r.n, r.boundary); err != nil {
			f.t.Fatalf("read %d: %v", f.reads, err)
		}
	}
}

// flushTail emits the buffered bytes as runEncodePipeline's idle flush does.
func (f *auTestFeed) flushTail() {
	f.t.Helper()
	tail, ok := flushAnnexBTail(f.buf)
	if !ok {
		f.t.Fatalf("idle flush found no unit in % x", f.buf)
	}
	f.units = append(f.units, tail)
	f.at = append(f.at, f.reads-1)
	f.buf = f.buf[:0]
	f.tracker.forget()
}

// checkUnits asserts that the emitted units plus the buffered tail are
// exactly the stream's NAL units: early emission never moves a boundary.
func (f *auTestFeed) checkUnits(want [][]byte) {
	f.t.Helper()
	got := f.units
	if len(f.buf) > 0 {
		got = append(append([][]byte(nil), got...), f.buf)
	}
	if len(got) != len(want) {
		f.t.Fatalf("got %d units, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			f.t.Fatalf("unit %d differs: got %d bytes, want %d", i, len(got[i]), len(want[i]))
		}
	}
}

// releasedAt reports the read that released unit i, or -1 if still buffered.
func (f *auTestFeed) releasedAt(i int) int {
	if i < len(f.at) {
		return f.at[i]
	}
	return -1
}

func TestAccessUnitTrackerSendsSmallFramesWithTheirLastWrite(t *testing.T) {
	s := newAUTestStream(
		auTestIDR(3000),
		auTestP(1500),
		[][]byte{auTestAUD, auTestSlice(true, false, true, 800)},
		[][]byte{auTestSEI, auTestSlice(true, false, true, 600)},
		auTestP(2000),
	)
	f := newAUTestFeed(t, s.data)
	f.feed(readsPerWrite(s.writes, auTestBoundaryLimit))
	f.checkUnits(s.nals())
	if len(f.buf) != 0 {
		t.Fatalf("%d bytes held back after the last frame's final write", len(f.buf))
	}
	for k := range s.aus {
		if got := f.releasedAt(s.lastNAL(k)); got != k {
			t.Errorf("frame %d's last NAL released at read %d, want %d (its own write)", k, got, k)
		}
	}
}

func TestAccessUnitTrackerWaitsForTheFinalWriteOfLargeFrames(t *testing.T) {
	s := newAUTestStream(auTestIDR(50000), auTestP(1200))
	if len(s.writes) != 3 || len(s.writes[0]) != ffmpegAVIOBufferSize {
		t.Fatalf("unexpected write layout %d", len(s.writes))
	}
	f := newAUTestFeed(t, s.data)
	// Claim boundary proof even for the full-buffer write: the write size
	// alone must keep the frame open.
	for _, w := range s.writes {
		if err := f.read(len(w), true); err != nil {
			t.Fatal(err)
		}
	}
	f.checkUnits(s.nals())
	if got := f.releasedAt(s.lastNAL(0)); got != 1 {
		t.Errorf("large frame's slice released at read %d, want 1 (its final write)", got)
	}
	if got := f.releasedAt(s.lastNAL(1)); got != 2 {
		t.Errorf("next frame's slice released at read %d, want 2", got)
	}
}

func TestAccessUnitTrackerSendsLargeFramesReadWhole(t *testing.T) {
	// The reader usually takes a full-buffer write and the short write
	// right behind it in one read; that read ends the frame.
	s := newAUTestStream(auTestIDR(50000), auTestP(1200), auTestP(40000))
	f := newAUTestFeed(t, s.data)
	f.feed([]auTestRead{{n: 50000, boundary: true}, {n: 1200, boundary: true}, {n: 40000, boundary: true}})
	f.checkUnits(s.nals())
	for k := range s.aus {
		if got := f.releasedAt(s.lastNAL(k)); got != k {
			t.Errorf("frame %d released at read %d, want %d (its own read)", k, got, k)
		}
	}
}

func TestAccessUnitTrackerHoldsBackWithoutBoundaryProof(t *testing.T) {
	s := newAUTestStream(auTestIDR(3000), auTestP(900), auTestP(700), auTestP(1100))
	reads := readsPerWrite(s.writes, auTestBoundaryLimit)
	for i := 0; i < 3; i++ {
		reads[i].boundary = false
	}
	f := newAUTestFeed(t, s.data)
	f.feed(reads)
	f.checkUnits(s.nals())
	for k := 0; k < 3; k++ {
		if got := f.releasedAt(s.lastNAL(k)); got != k+1 {
			t.Errorf("unproven frame %d released at read %d, want %d (next frame's start code)", k, got, k+1)
		}
	}
	// The access-unit start was followed through the NAL sequence, so the
	// first proven boundary sends its frame at once.
	if got := f.releasedAt(s.lastNAL(3)); got != 3 {
		t.Errorf("proven frame released at read %d, want 3", got)
	}
}

func TestAccessUnitTrackerHoldsBackFramesFillingWholeWrites(t *testing.T) {
	// A 65536-byte frame leaves as two full-buffer writes: no short write
	// marks its end, so it waits for the next frame as before.
	s := newAUTestStream(auTestIDR(2*ffmpegAVIOBufferSize), auTestP(1000))
	if len(s.writes) != 3 {
		t.Fatalf("got %d writes, want 3", len(s.writes))
	}
	f := newAUTestFeed(t, s.data)
	for _, w := range s.writes {
		if err := f.read(len(w), true); err != nil {
			t.Fatal(err)
		}
	}
	f.checkUnits(s.nals())
	if got := f.releasedAt(s.lastNAL(0)); got != 2 {
		t.Errorf("whole-write frame released at read %d, want 2", got)
	}
	if got := f.releasedAt(s.lastNAL(1)); got != 2 {
		t.Errorf("following frame released at read %d, want 2", got)
	}
}

func TestAccessUnitTrackerHandlesSeveralFramesPerRead(t *testing.T) {
	s := newAUTestStream(auTestIDR(4000), auTestP(500), auTestP(600))
	f := newAUTestFeed(t, s.data)
	f.feed([]auTestRead{{n: len(s.data), boundary: true}})
	f.checkUnits(s.nals())
	if len(f.buf) != 0 {
		t.Fatalf("%d bytes held back after a proven boundary", len(f.buf))
	}
}

func TestAccessUnitTrackerDetectsBrokenWritePattern(t *testing.T) {
	// A writer that splits a frame short of a full buffer breaks the
	// pattern; the cut slice must be reported, not passed on as complete.
	s := newAUTestStream(auTestIDR(3000), auTestP(800))
	f := newAUTestFeed(t, s.data)
	if err := f.read(1500, true); err != nil {
		t.Fatalf("first read: %v", err)
	}
	err := f.read(1500, true)
	if !errors.Is(err, errAccessUnitResync) {
		t.Fatalf("mid-NAL continuation after an early emission: err = %v, want errAccessUnitResync", err)
	}
}

func TestAccessUnitTrackerAcceptsStartCodeSplitAcrossReads(t *testing.T) {
	s := newAUTestStream(auTestIDR(2000), auTestP(900))
	f := newAUTestFeed(t, s.data)
	f.feed([]auTestRead{
		{n: len(s.writes[0]), boundary: true},
		{n: 2, boundary: false}, // 00 00
		{n: len(s.writes[1]) - 2, boundary: true},
	})
	f.checkUnits(s.nals())
	if got := f.releasedAt(s.lastNAL(1)); got != 2 {
		t.Errorf("frame after split start code released at read %d, want 2", got)
	}
}

func TestAccessUnitTrackerForgetWaitsForTheNextFrameStart(t *testing.T) {
	s := newAUTestStream(auTestIDR(2500), auTestP(700), auTestP(900), auTestP(600))
	f := newAUTestFeed(t, s.data)
	f.feed([]auTestRead{{n: len(s.writes[0]), boundary: false}})
	f.flushTail() // the idle flush took the buffered slice
	f.feed(readsPerWrite(s.writes[1:], auTestBoundaryLimit))
	f.checkUnits(s.nals())
	// After the idle flush the frame start is unknown: frame 1 is held
	// until frame 2 reveals where frames start again.
	if got := f.releasedAt(s.lastNAL(1)); got != 2 {
		t.Errorf("frame after idle flush released at read %d, want 2", got)
	}
	if got := f.releasedAt(s.lastNAL(2)); got != 2 {
		t.Errorf("frame 2 released at read %d, want 2", got)
	}
	if got := f.releasedAt(s.lastNAL(3)); got != 3 {
		t.Errorf("frame 3 released at read %d, want 3", got)
	}
}

func TestAccessUnitTrackerIgnoresBytesWithoutStartCodes(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB}, 300)
	f := newAUTestFeed(t, data)
	f.feed([]auTestRead{{n: 100, boundary: true}, {n: 200, boundary: true}})
	if len(f.units) != 0 || len(f.buf) != len(data) {
		t.Fatalf("emitted %d units, buffered %d bytes; want none and all", len(f.units), len(f.buf))
	}
}

func TestLeadingStartCode(t *testing.T) {
	cases := []struct {
		in   []byte
		want startCodeCheck
	}{
		{nil, startCodeIncomplete},
		{[]byte{0}, startCodeIncomplete},
		{[]byte{0, 0, 0}, startCodeIncomplete},
		{[]byte{0, 0, 1}, startCodePresent},
		{[]byte{0, 0, 0, 1, 0x41}, startCodePresent},
		{[]byte{0, 0, 0, 0, 1, 0x41}, startCodePresent},
		{[]byte{0, 1, 0x41}, startCodeAbsent},
		{[]byte{0, 0, 3, 1}, startCodeAbsent},
		{[]byte{1, 0x41}, startCodeAbsent},
		{[]byte{0xA5, 0, 0, 1}, startCodeAbsent},
	}
	for _, c := range cases {
		if got := leadingStartCode(c.in); got != c.want {
			t.Errorf("leadingStartCode(% x) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestOpensAccessUnit(t *testing.T) {
	for _, nalType := range []byte{6, 7, 8, 9, 14, 15, 16, 17, 18} {
		if !opensAccessUnit(nalType, nil) {
			t.Errorf("type %d after a slice must open an access unit", nalType)
		}
	}
	for _, nalType := range []byte{2, 3, 4, 10, 11, 12, 13, 19, 20} {
		if opensAccessUnit(nalType, []byte{0x88}) {
			t.Errorf("type %d must not open an access unit", nalType)
		}
	}
	for _, nalType := range []byte{1, 5} {
		if !opensAccessUnit(nalType, []byte{0x88}) {
			t.Errorf("type %d with first_mb_in_slice 0 must open an access unit", nalType)
		}
		if opensAccessUnit(nalType, []byte{0x40}) {
			t.Errorf("type %d continuing a picture must not open an access unit", nalType)
		}
		if opensAccessUnit(nalType, nil) {
			t.Errorf("type %d without slice header must not open an access unit", nalType)
		}
	}
}

func TestEarlyEmissionNeedsDeclaredPatternAndProof(t *testing.T) {
	prevHook, prevDisabled := preparePipeWriteBoundaries, auEarlyFlushDisabled.Load()
	t.Cleanup(func() {
		preparePipeWriteBoundaries = prevHook
		auEarlyFlushDisabled.Store(prevDisabled)
	})
	auEarlyFlushDisabled.Store(false)
	declared := pipelineSpec{AUWriteChunk: ffmpegAVIOBufferSize}
	prepared := 0
	preparePipeWriteBoundaries = func(_ io.Reader, readSize int) int {
		prepared++
		if readSize != pipelineReadSize {
			t.Errorf("pipe prepared for %d-byte reads, want %d", readSize, pipelineReadSize)
		}
		return auTestBoundaryLimit
	}

	if limit := fullPipeBoundFor(declared, nil); limit != auTestBoundaryLimit {
		t.Fatalf("declared pattern: bound %d, want %d", limit, auTestBoundaryLimit)
	}
	if accessUnitTrackerFor(declared, auTestBoundaryLimit) == nil {
		t.Fatal("no tracker for a declared pattern with boundary proof")
	}
	if accessUnitTrackerFor(declared, 0) != nil {
		t.Error("tracker enabled without boundary proof")
	}
	// Pipes of other pipelines are left alone.
	if limit := fullPipeBoundFor(pipelineSpec{}, nil); limit != 0 || prepared != 1 {
		t.Errorf("undeclared pattern: limit %d, pipes prepared %d; want 0, 1", limit, prepared)
	}
	if accessUnitTrackerFor(pipelineSpec{}, auTestBoundaryLimit) != nil {
		t.Error("tracker enabled for a spec without a declared write pattern")
	}
	auEarlyFlushDisabled.Store(true)
	if limit := fullPipeBoundFor(declared, nil); limit != 0 || prepared != 1 {
		t.Errorf("after a misprediction: limit %d, pipes prepared %d; want 0, 1", limit, prepared)
	}
	if accessUnitTrackerFor(declared, auTestBoundaryLimit) != nil {
		t.Error("tracker enabled after a misprediction disabled early emission")
	}
	auEarlyFlushDisabled.Store(false)
	preparePipeWriteBoundaries = nil
	if limit := fullPipeBoundFor(declared, nil); limit != 0 {
		t.Errorf("platform without boundary proof: limit %d, want 0", limit)
	}
}

func TestBoundaryReadsCountBytesSinceTheLastDrain(t *testing.T) {
	const r = pipelineReadSize
	cases := []struct {
		name  string
		bound int
		reads []int
		want  []bool
	}{
		{"grown pipe", 126976,
			[]int{50000, r - 1, r, 61440, r, 61441, 100},
			[]bool{true, true, false, true, false, false, true}},
		{"after two full reads", 126976,
			[]int{r, r, 1, 1},
			[]bool{false, false, false, true}},
		{"default pipe", auTestBoundaryLimit,
			[]int{auTestBoundaryLimit, auTestBoundaryLimit + 1, r, 1, 1},
			[]bool{true, false, false, false, true}},
		{"no bound", 0, []int{1, r, 1}, []bool{false, false, false}},
	}
	for _, c := range cases {
		proofs := boundaryReads{fullPipe: c.bound}
		for i, n := range c.reads {
			if got := proofs.next(n, r); got != c.want[i] {
				t.Errorf("%s: read %d (%d bytes) proven = %v, want %v", c.name, i, n, got, c.want[i])
			}
		}
	}
}
