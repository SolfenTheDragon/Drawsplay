package main

// Process resilience helpers: panic containment for per-session goroutines
// and duplicate-instance detection.

import (
	"fmt"
	"log"
	"net"
	"runtime/debug"
	"sync"
	"time"
)

// logPanic is a deferred guard for per-session goroutines: a panic in one
// client session (malformed traffic, an injector edge case, a driver quirk
// on an unusual system) is logged with a stack trace instead of killing the
// whole host. Listeners keep accepting, so the client simply reconnects.
func logPanic(label string) {
	if r := recover(); r != nil {
		log.Printf("PANIC in %s (recovered, session dropped): %v\n%s", label, r, debug.Stack())
	}
}

// throttledLog rate-limits repetitive log lines by key. A stale client
// instance can probe the listeners several times per second indefinitely;
// unthrottled, those messages drown the log lines that matter. The first
// occurrence logs immediately; repeats within the interval are counted and
// summarized when the window reopens.
type throttledLogState struct {
	lastLoggedAt time.Time
	suppressed   int
}

var throttledLogs = struct {
	sync.Mutex
	byKey map[string]*throttledLogState
}{byKey: map[string]*throttledLogState{}}

const throttledLogInterval = 30 * time.Second

func throttledLog(key, format string, args ...any) {
	suppressed, ok := throttledLogAllowed(key, time.Now())
	if !ok {
		return
	}
	if suppressed > 0 {
		log.Printf(format+" (%d similar suppressed in the last %s)",
			append(args, suppressed, throttledLogInterval)...)
		return
	}
	log.Printf(format, args...)
}

// throttledLogAllowed reports whether a line keyed by key may log at now,
// and how many occurrences were suppressed since the last logged one.
func throttledLogAllowed(key string, now time.Time) (int, bool) {
	throttledLogs.Lock()
	defer throttledLogs.Unlock()
	state, ok := throttledLogs.byKey[key]
	if !ok {
		state = &throttledLogState{}
		throttledLogs.byKey[key] = state
	}
	if !state.lastLoggedAt.IsZero() && now.Sub(state.lastLoggedAt) < throttledLogInterval {
		state.suppressed++
		return 0, false
	}
	suppressed := state.suppressed
	state.suppressed = 0
	state.lastLoggedAt = now
	return suppressed, true
}

// anotherInstanceRunning reports whether a DrawSplay host already owns the
// local ports, so a second launch can explain itself instead of dumping a
// raw bind error.
func anotherInstanceRunning() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", webuiPort), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
