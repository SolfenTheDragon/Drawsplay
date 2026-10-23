package main

import (
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"drawsplay/host/internal/wifilan"
)

const (
	// wifiPairedInterfaceEnv names the explicit, directly connected LAN
	// interface for paired Wi-Fi sessions; there is no automatic selection.
	wifiPairedInterfaceEnv = "DRAWSPLAY_WIFI_INTERFACE"
	// wifiTrustDirName is created by `wifi-pair`, never by this startup path.
	wifiTrustDirName = "wifi-trust"
)

// startPairedWiFi starts paired Wi-Fi discovery and sessions. It returns false
// and does nothing unless the feature flag is on, an interface is named and a
// pairing already exists; USB is never affected.
func startPairedWiFi(ctx context.Context, store *settingsStore) bool {
	if !wifiFeatureEnabled() {
		return false
	}
	iface := strings.TrimSpace(os.Getenv(wifiPairedInterfaceEnv))
	if iface == "" {
		return false
	}
	base, err := configDir()
	if err != nil {
		return false
	}
	dir := filepath.Join(base, wifiTrustDirName)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	trust, err := wifilan.OpenTrustStore(dir)
	if err != nil {
		log.Printf("Paired Wi-Fi disabled: cannot open trust store: %v", err)
		return false
	}
	if _, paired := trust.Peer(); !paired {
		trust.Close()
		return false
	}
	inputs, videos := newWiFiConnListener(), newWiFiConnListener()
	go func() {
		<-ctx.Done()
		inputs.Close()
		videos.Close()
		trust.Close()
	}()
	go runInputListener(ctx, inputs, store, transportWiFiPaired)
	go runVideoListenerAt(ctx, store, "wifi-session", transportWiFiPaired, func(string) (net.Listener, error) { return videos, nil })
	manager := &wifiSessionManager{
		enabled: true, retryDelay: 3 * time.Second, inputs: inputs, videos: videos,
		owner: hostSessionOwnership.currentTransport,
		browse: func(ctx context.Context, found func(wifilan.Candidate)) error {
			return wifilan.Browse(ctx, iface, found, func(err error) { throttledLog("wifi-reject", "Wi-Fi discovery ignored an advertisement: %v", err) })
		},
		establish: func(ctx context.Context, c wifilan.Candidate) (net.Conn, net.Conn, error) {
			return establishPairedWiFi(ctx, iface, trust, c)
		},
	}
	go manager.run(ctx)
	log.Printf("Paired Wi-Fi enabled on interface %q", iface)
	return true
}
