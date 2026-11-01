package main

import (
	"fmt"
	"net/http"
	"sync"
)

// Open settings pages hold a server-sent-events stream here. The tray uses
// it to raise an already-open page instead of launching a new browser tab.
var settingsPages = struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}{clients: map[chan struct{}]struct{}{}}

// focusOpenSettingsPage asks every open settings page to focus itself and
// reports whether at least one page was listening.
func focusOpenSettingsPage() bool {
	settingsPages.mu.Lock()
	defer settingsPages.mu.Unlock()
	for ch := range settingsPages.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return len(settingsPages.clients) > 0
}

func handleFocusEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch := make(chan struct{}, 1)
	settingsPages.mu.Lock()
	settingsPages.clients[ch] = struct{}{}
	settingsPages.mu.Unlock()
	defer func() {
		settingsPages.mu.Lock()
		delete(settingsPages.clients, ch)
		settingsPages.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, ": ok\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprint(w, "event: focus\ndata: 1\n\n")
			fl.Flush()
		}
	}
}
