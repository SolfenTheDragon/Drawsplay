package main

import (
	"io"
	"log"
	"strings"
	"sync"
)

const (
	debugLogMaxLines = 1000
	debugLogMaxChars = 4000
)

type debugLogBuffer struct {
	mu      sync.Mutex
	startID int
	lines   []string
}

func (b *debugLogBuffer) append(line string) {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return
	}
	if len(line) > debugLogMaxChars {
		line = line[:debugLogMaxChars] + "…"
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > debugLogMaxLines {
		trim := len(b.lines) - debugLogMaxLines
		b.lines = b.lines[trim:]
		b.startID += trim
	}
}

func (b *debugLogBuffer) since(id int) ([]string, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if id < b.startID {
		id = b.startID
	}
	offset := id - b.startID
	if offset < 0 {
		offset = 0
	}
	if offset > len(b.lines) {
		offset = len(b.lines)
	}
	out := append([]string(nil), b.lines[offset:]...)
	return out, b.startID + len(b.lines)
}

var uiDebugLogs debugLogBuffer
var installDebugLogCaptureOnce sync.Once

type debugLogWriter struct {
	mu      sync.Mutex
	partial string
}

func (w *debugLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.partial += string(p)
	for {
		idx := strings.IndexByte(w.partial, '\n')
		if idx < 0 {
			break
		}
		uiDebugLogs.append(w.partial[:idx])
		w.partial = w.partial[idx+1:]
	}
	return len(p), nil
}

func installDebugLogCapture() {
	installDebugLogCaptureOnce.Do(func() {
		current := log.Writer()
		log.SetOutput(io.MultiWriter(current, &debugLogWriter{}))
	})
}

func currentDebugLogs(since int) ([]string, int) {
	if since < 0 {
		since = 0
	}
	return uiDebugLogs.since(since)
}

func init() {
	installDebugLogCapture()
}
