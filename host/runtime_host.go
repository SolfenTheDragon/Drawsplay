package main

import (
	"context"
	"fmt"
	"log"
)

// runHostRuntime starts all host listeners and blocks until the runtime
// stops. The stop callback is wired into the Web UI /api/stop endpoint.
func runHostRuntime(ctx context.Context, store *settingsStore, stop func()) error {
	if ok, err := acquireInstanceLock(); !ok {
		return fmt.Errorf("another DrawSplay host is already running (settings UI at http://127.0.0.1:%d)", webuiPort)
	} else if err != nil {
		log.Printf("Single-instance lock unavailable (%v); continuing", err)
	}
	cfg := store.get()
	runDependencyPreflight(cfg)
	recoverDisplayGuard()
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

	removeLegacyWiFiFiles()
	// Paired mutual-TLS Wi-Fi: inert unless DRAWSPLAY_FEATURE_WIFI, an
	// explicit interface and an existing pairing are all present.
	pairedWiFiRuntime.init(ctx, store)
	wifiPairing = newHostWiFiPairingService(store)

	// Default USB transport: Android Open Accessory, without ADB or debugging.
	startAOATransport(ctx, store)

	// Loopback listeners for the optional adb reverse tunnel: open only while
	// that setting is on (see adb_loopback.go).
	go runADBLoopbackListeners(ctx, store)
	log.Printf("Waiting for client (USB accessory; ADB reverse is opt-in)")

	<-ctx.Done()
	return nil
}
