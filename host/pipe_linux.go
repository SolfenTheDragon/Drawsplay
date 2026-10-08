//go:build linux

package main

import (
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func init() {
	preparePipeWriteBoundaries = linuxPreparePipeWriteBoundaries
}

// linuxPreparePipeWriteBoundaries grows a fresh pipe to pipeBoundaryCapacity
// and returns its full bound; 0 for anything but a pipe. Growing is best
// effort: past fs.pipe-user-pages-soft an unprivileged user keeps the capacity
// the pipe has, and so a lower bound (0, leaving early emission off, for the
// minimum-size pipes the kernel then creates).
func linuxPreparePipeWriteBoundaries(r io.Reader, readSize int) int {
	conn, ok := r.(syscall.Conn)
	if !ok {
		return 0
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0
	}
	pageSize := os.Getpagesize()
	pipeBytes := 0
	if err := raw.Control(func(fd uintptr) {
		n, err := unix.FcntlInt(fd, unix.F_GETPIPE_SZ, 0)
		if err != nil {
			return
		}
		if want := pipeBoundaryCapacity(pageSize, readSize); n < want {
			if grown, err := unix.FcntlInt(fd, unix.F_SETPIPE_SZ, want); err == nil {
				n = grown
			}
		}
		pipeBytes = n
	}); err != nil {
		return 0
	}
	return pipeFullBound(pipeBytes, pageSize)
}

// pipeBoundaryCapacity is the smallest pipe capacity whose full bound covers
// every short read of readSize bytes after a short read: (S-1)/2 pages of at
// least readSize-1 bytes. The kernel rounds it up to a power of two, 256 KiB
// for 64 KiB reads of 4 KiB pages, whose bound still covers reads up to 60 KiB
// right after a full read. Growing adds at most that much stall buffering to
// the read channel and the 1 MiB socket buffer.
func pipeBoundaryCapacity(pageSize, readSize int) int {
	pagesPerRead := (readSize - 1 + pageSize - 1) / pageSize
	return (2*pagesPerRead + 1) * pageSize
}

// pipeFullBound returns B for a Linux pipe of pipeBytes capacity: its writer
// only stops mid-write while the pipe holds more than B bytes.
//
// pipe_write copies a write while holding the pipe mutex and lets go of it
// mid-write only to sleep on a full pipe, where a signal can also end the
// write early (the only short write; the encoder writes the rest next).
// pipe_read takes everything buffered, up to its size, under the same mutex.
// A full pipe has all S page slots in use, and the rules that merge writes
// into the last slot never leave two adjacent slots (bar a partly read first
// one) holding a page or less between them, so it holds more than (S-1)/2
// pages. The capacity must stay fixed while the writer runs.
func pipeFullBound(pipeBytes, pageSize int) int {
	if pipeBytes <= 0 || pageSize <= 0 || pipeBytes%pageSize != 0 {
		return 0
	}
	return (pipeBytes/pageSize - 1) / 2 * pageSize
}
