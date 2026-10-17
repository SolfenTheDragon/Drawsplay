//go:build windows

package main

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func init() {
	preparePipeWriteBoundaries = windowsPreparePipeWriteBoundaries
}

// windowsPipeFullBound is the bound reported for Windows pipes: larger than
// any amount of data a pipeline's stdout carries between two short reads, yet
// small enough that boundaryReads arithmetic cannot overflow an int.
const windowsPipeFullBound = 1 << 30

// windowsPreparePipeWriteBoundaries reports an unlimited bound for an
// anonymous (byte-mode) pipe; 0 for anything else.
//
// The named-pipe file system queues each WriteFile whole while holding the
// pipe's lock: what does not fit the pipe's quota stays queued as part of
// that same write, which pends until a reader takes it. ReadFile copies
// queued bytes, across writes, until its buffer is full or the queue is
// empty. So a reader can never see a writer stopped partway through a write:
// a read shorter than its buffer emptied the queue and ended where a write
// ended. Each ffmpeg write(2) is one WriteFile on a binary-mode pipe.
func windowsPreparePipeWriteBoundaries(r io.Reader, _ int) int {
	f, ok := r.(*os.File)
	if !ok {
		return 0
	}
	t, err := windows.GetFileType(windows.Handle(f.Fd()))
	if err != nil || t != windows.FILE_TYPE_PIPE {
		return 0
	}
	return windowsPipeFullBound
}
