package main

// eventLogger decouples per-event diagnostic logging from the network read
// path. Console writes can be slow (especially on Windows); logging goes
// through a bounded channel and lines are dropped (and counted) instead of
// ever blocking input processing. This upholds the latency rule that
// diagnostics must never back-pressure the input path (CODE_STANDARDS.md §5.1).

import (
	"log"
	"os"
	"strings"
	"sync/atomic"
)

const eventLogBuffer = 2048

var eventDiagnostics atomic.Bool

func init() {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("DRAW_SPLAY_EVENT_LOG")))
	eventDiagnostics.Store(raw == "1" || raw == "true" || raw == "yes")
}

func eventDiagnosticsEnabled() bool { return eventDiagnostics.Load() }

type eventLogger struct {
	ch      chan string
	dropped atomic.Uint64
}

// newEventLogger starts the single consumer goroutine and returns the logger.
func newEventLogger(buffer int) *eventLogger {
	l := &eventLogger{ch: make(chan string, buffer)}
	go l.run()
	return l
}

func (l *eventLogger) run() {
	for line := range l.ch {
		log.Print(line)
		if d := l.dropped.Swap(0); d > 0 {
			log.Printf("Event logging backlog: dropped %d diagnostic lines (input processing was not affected)", d)
		}
	}
}

// enqueue submits a line without ever blocking; overflow is counted instead.
func (l *eventLogger) enqueue(line string) {
	select {
	case l.ch <- line:
	default:
		l.dropped.Add(1)
	}
}
