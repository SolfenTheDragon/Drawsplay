package main

// Web UI (settings frontend).
//
// A localhost-only HTTP server (port 27180) serving an embedded single-page
// settings UI plus a small JSON API:
//
//	GET  /                — embedded index.html
//	GET  /api/settings    — current settings
//	PUT  /api/settings    — validate + persist + apply (restarts the active
//	                        video session so changes take effect immediately)
//	GET  /api/status      — streaming state and autostart registration
//	POST /api/autostart   — {"enabled": true|false} registers/unregisters
//	                        the login autostart (see service_*.go)
//	POST /api/tablet/clear-display-mode
//	                      — asks connected client sessions to clear their
//	                        stored preferred display resolution
//	POST /api/stop        — gracefully shuts the host process down
//
// Security model: bound to 127.0.0.1 only. State changes additionally require
// a per-process CSRF header token, and browser origins must match the loopback
// UI authority to prevent hostile pages from driving the local API.

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

//go:embed webui
var webuiFS embed.FS

// runWebUI serves the settings UI until ctx is cancelled. Best-effort like
// the video listener: a busy port disables the UI but the host keeps working.
// stop triggers a graceful process shutdown (the /api/stop endpoint).
func runWebUI(ctx context.Context, store *settingsStore, stop func()) {
	addr := fmt.Sprintf("127.0.0.1:%d", webuiPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("Web UI disabled: %v", err)
		return
	}
	log.Printf("Web UI on http://%s", addr)

	server := &http.Server{
		Handler:           newWebUIHandler(store, stop),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("Web UI server ended: %v", err)
	}
}
