package main

import "sync"

// latest holds the most recent value of something the host reports (a
// transport's status, the video status, the tablet's last stats) for readers
// on other goroutines: the settings page, diagnostics and the trays.
// Values are copied in and out, so T must not share mutable memory with its
// writer (copy slices and pointers before Set).
type latest[T any] struct {
	mu sync.RWMutex
	v  T
}

func (l *latest[T]) Set(v T) {
	l.mu.Lock()
	l.v = v
	l.mu.Unlock()
}

func (l *latest[T]) Get() T {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.v
}

// Update changes the value in place, atomically with respect to other calls.
func (l *latest[T]) Update(change func(*T)) {
	l.mu.Lock()
	change(&l.v)
	l.mu.Unlock()
}
