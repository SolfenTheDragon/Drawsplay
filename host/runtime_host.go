package main

import (
	"context"
	"fmt"
	"log"
	"net"
)

// runHostRuntime starts all host listeners and blocks until the runtime
// stops. The stop callback is wired into the Web UI /api/stop endpoint.
func runHostRuntime(ctx context.Context, store *settingsStore, stop func()) error {
	cfg := store.get()
	runDependencyPreflight(cfg)
	ensureX11VirtualDisplayAsync("host start", false)
	ensureVirtualDisplayDriver(cfg.NoDriverInstall)

	// Maintain the optional legacy ADB transport while allowing settings to
	// enable or disable it at runtime. It is off by default; AOA is primary.
	go maintainADBReverse(ctx, store, inputPort, videoPort)

	// Settings web UI (localhost only; best-effort like the video channel).
	go runWebUI(ctx, store, stop)
	startSessionLockMonitor(ctx)
	go releaseStaleVirtualDisplays()
	startDisplayWatchdog(ctx, store)

	// Video channel (best-effort: logs and disables itself if ffmpeg or the
	// port is unavailable; input continues to work regardless).
	go runVideoListener(ctx, store)

	// Optional Wi-Fi transport is additive and disabled by default so USB
	// behavior and performance stay unchanged.
	go startWiFiTransport(ctx, store)
	// Paired mutual-TLS Wi-Fi: inert unless DRAWSPLAY_FEATURE_WIFI, an
	// explicit interface and an existing pairing are all present.
	pairedWiFiRuntime.init(ctx, store)
	wifiPairing = newHostWiFiPairingService(store)

	// Default USB transport: Android Open Accessory, without ADB or debugging.
	startAOATransport(ctx, store)

	// Input channel (blocking; owns process lifetime).
	addr := fmt.Sprintf("127.0.0.1:%d", inputPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if anotherInstanceRunning() {
			return fmt.Errorf("another DrawSplay host is already running (settings UI at http://127.0.0.1:%d)", webuiPort)
		}
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	defer ln.Close()
	log.Printf("Input listener on %s", addr)
	log.Printf("Waiting for client (USB accessory; ADB reverse is opt-in)")

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	runInputListener(ctx, ln, store, "usb")
	return nil
}
