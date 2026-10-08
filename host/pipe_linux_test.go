//go:build linux

package main

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// pipeCapacity reads a pipe's capacity without switching it to blocking mode,
// as File.Fd would.
func pipeCapacity(t *testing.T, f *os.File) int {
	t.Helper()
	raw, err := f.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	size := 0
	var ferr error
	if err := raw.Control(func(fd uintptr) { size, ferr = unix.FcntlInt(fd, unix.F_GETPIPE_SZ, 0) }); err != nil {
		t.Fatal(err)
	}
	if ferr != nil {
		t.Fatalf("F_GETPIPE_SZ: %v", ferr)
	}
	return size
}

func TestPipeFullBound(t *testing.T) {
	cases := []struct{ pipe, page, want int }{
		{65536, 4096, 28672},   // default pipe: 16 slots
		{262144, 4096, 126976}, // grown for 64 KiB reads: 64 slots
		{16384, 4096, 4096},
		{8192, 4096, 0}, // two slots bound nothing
		{4096, 4096, 0},
		{1 << 20, 65536, 458752}, // default pipe of 64 KiB pages
		{65536, 65536, 0},
		{65537, 4096, 0},
		{0, 4096, 0},
		{65536, 0, 0},
	}
	for _, c := range cases {
		if got := pipeFullBound(c.pipe, c.page); got != c.want {
			t.Errorf("pipeFullBound(%d, %d) = %d, want %d", c.pipe, c.page, got, c.want)
		}
	}
}

func TestPipeBoundaryCapacityIsTheSmallestCoveringShortReads(t *testing.T) {
	for _, page := range []int{4096, 16384, 65536} {
		for _, read := range []int{4096, 65536, 100000} {
			c := pipeBoundaryCapacity(page, read)
			if got := pipeFullBound(c, page); got < read-1 {
				t.Errorf("page %d, reads of %d: capacity %d bounds %d, want >= %d", page, read, c, got, read-1)
			}
			if got := pipeFullBound(c-2*page, page); got >= read-1 {
				t.Errorf("page %d, reads of %d: capacity %d is not the smallest (%d also bounds %d)", page, read, c, c-2*page, got)
			}
		}
	}
	if got := pipeBoundaryCapacity(4096, pipelineReadSize); got != 33*4096 {
		t.Errorf("capacity for 64 KiB reads of 4 KiB pages = %d, want 33 pages (256 KiB once rounded)", got)
	}
}

func TestLinuxPreparePipeWriteBoundariesGrowsThePipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	page := os.Getpagesize()
	bound := linuxPreparePipeWriteBoundaries(r, pipelineReadSize)
	size := pipeCapacity(t, r)
	if want := pipeFullBound(size, page); bound != want {
		t.Fatalf("bound for a %d-byte pipe = %d, want %d", size, bound, want)
	}
	if want := pipeBoundaryCapacity(page, pipelineReadSize); size < want {
		t.Skipf("pipe stayed at %d bytes, under the per-user pipe limits? want >= %d", size, want)
	}
	if bound < pipelineReadSize-1 {
		t.Errorf("grown pipe bound %d does not cover short reads of %d-byte buffers", bound, pipelineReadSize)
	}

	if got := linuxPreparePipeWriteBoundaries(bytes.NewReader(nil), pipelineReadSize); got != 0 {
		t.Errorf("bound for a non-file reader = %d, want 0", got)
	}
	f, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := linuxPreparePipeWriteBoundaries(f, pipelineReadSize); got != 0 {
		t.Errorf("bound for a regular file = %d, want 0", got)
	}
}

func TestLinuxPreparePipeWriteBoundariesNeverShrinks(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	raw, err := r.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var serr error
	if err := raw.Control(func(fd uintptr) { _, serr = unix.FcntlInt(fd, unix.F_SETPIPE_SZ, 1<<20) }); err != nil {
		t.Fatal(err)
	}
	if serr != nil {
		t.Skipf("cannot grow a pipe to 1 MiB: %v", serr)
	}
	if got := linuxPreparePipeWriteBoundaries(r, pipelineReadSize); got != pipeFullBound(1<<20, os.Getpagesize()) {
		t.Errorf("bound for a 1 MiB pipe = %d", got)
	}
	if size := pipeCapacity(t, r); size != 1<<20 {
		t.Errorf("1 MiB pipe resized to %d bytes", size)
	}
}

// TestPipeProvenReadsEndAtWriteBoundaries checks the kernel behaviour behind
// pipeFullBound and boundaryReads: no proven read returns part of a write,
// including writes larger than the pipe, of the default and the grown pipe.
func TestPipeProvenReadsEndAtWriteBoundaries(t *testing.T) {
	for _, grow := range []bool{false, true} {
		name := "default"
		if grow {
			name = "grown"
		}
		t.Run(name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			page := os.Getpagesize()
			bound := pipeFullBound(pipeCapacity(t, r), page)
			if grow {
				bound = linuxPreparePipeWriteBoundaries(r, pipelineReadSize)
				if pipeCapacity(t, r) < pipeBoundaryCapacity(page, pipelineReadSize) {
					t.Skip("the pipe could not be grown")
				}
			}
			if bound == 0 {
				t.Skip("this pipe carries no boundary proof")
			}
			checkProvenReadsEndAtWriteBoundaries(t, r, w, bound, pipeCapacity(t, r))
		})
	}
}

func checkProvenReadsEndAtWriteBoundaries(t *testing.T, r, w *os.File, bound, capacity int) {
	wfd := int(w.Fd()) // blocking writes, like an encoder's stdout
	rng := rand.New(rand.NewSource(1))
	sizes := make([]int, 2000)
	for i := range sizes {
		switch rng.Intn(4) {
		case 0:
			sizes[i] = ffmpegAVIOBufferSize
		case 1:
			sizes[i] = 1 + rng.Intn(bound)
		case 2:
			sizes[i] = 1 + rng.Intn(4096)
		default:
			sizes[i] = 1 + rng.Intn(2*capacity)
		}
	}
	writeEnds := make(chan map[int64]bool, 1)
	go func() {
		defer w.Close()
		ends := map[int64]bool{}
		payload := make([]byte, 2*capacity)
		var off int64
		for _, size := range sizes {
			// Like ffmpeg's AVIO, finish a write cut short by a signal
			// with another; only the end of the whole write counts.
			for p := payload[:size]; len(p) > 0; {
				n, err := syscall.Write(wfd, p)
				if err == syscall.EINTR {
					continue
				}
				if err != nil {
					writeEnds <- nil
					return
				}
				p = p[n:]
				off += int64(n)
			}
			ends[off] = true
		}
		writeEnds <- ends
	}()

	type readEnd struct {
		n      int
		off    int64
		proven bool
	}
	var reads []readEnd
	proofs := boundaryReads{fullPipe: bound}
	read := make([]byte, pipelineReadSize)
	var off int64
	for i := 0; ; i++ {
		n, err := r.Read(read)
		if n > 0 {
			off += int64(n)
			reads = append(reads, readEnd{n: n, off: off, proven: proofs.next(n, len(read))})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if i%16 == 0 {
			time.Sleep(time.Duration(rng.Intn(300)) * time.Microsecond) // let the pipe fill now and then
		}
	}
	ends := <-writeEnds
	if ends == nil {
		t.Fatal("writer failed")
	}
	proven, cut := 0, 0
	for _, rd := range reads {
		if rd.proven {
			proven++
			if !ends[rd.off] {
				t.Fatalf("a proven read (bound %d) ended at offset %d, inside a write", bound, rd.off)
			}
		}
		if rd.n < len(read) && !ends[rd.off] {
			cut++
		}
	}
	// Short reads that ended inside a write show what the proof excludes.
	t.Logf("capacity %d, bound %d: %d of %d reads proven; %d short reads ended inside a write", capacity, bound, proven, len(reads), cut)
}
