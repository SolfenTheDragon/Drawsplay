package main

import (
	"context"
	"log"
	"net"
	"os"
	"sync"
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

// pairedWiFiHandle is a running paired Wi-Fi runtime.
type pairedWiFiHandle struct {
	Label string
	stop  func()
}

// startPairedWiFiRuntime starts paired Wi-Fi discovery and sessions. It returns
// nil and does nothing unless the feature flag is on, the setting is on (when a
// settings store is supplied), an interface is named and a pairing already
// exists; USB is never affected. stop releases the trust-store lock before
// returning so pairing can take it.
func startPairedWiFiRuntime(parent context.Context, store *settingsStore) *pairedWiFiHandle {
	if !wifiFeatureEnabled() {
		return nil
	}
	if store != nil && !store.get().PairedWifiEnabled {
		return nil
	}
	iface := wifiPairedIface(store)
	if iface == "" {
		return nil
	}
	dir, err := wifiPairingTrustDir()
	if err != nil {
		return nil
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	trust, err := wifilan.OpenTrustStore(dir)
	if err != nil {
		log.Printf("Paired Wi-Fi disabled: cannot open trust store: %v", err)
		return nil
	}
	if _, paired := trust.Peer(); !paired {
		trust.Close()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	inputs, videos := newWiFiConnListener(), newWiFiConnListener()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			inputs.Close()
			videos.Close()
			trust.Close()
		})
	}
	go func() {
		<-ctx.Done()
		stop()
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
	return &pairedWiFiHandle{Label: trust.Label(), stop: stop}
}

// pairedWiFiController owns the runtime so settings changes and pairing can
// stop and restart it without restarting the host.
type pairedWiFiController struct {
	mu      sync.Mutex
	ctx     context.Context
	store   *settingsStore
	runtime *pairedWiFiHandle
}

var pairedWiFiRuntime = &pairedWiFiController{}

func (c *pairedWiFiController) init(ctx context.Context, store *settingsStore) {
	c.mu.Lock()
	c.ctx, c.store = ctx, store
	c.mu.Unlock()
	c.reconcile()
}

func (c *pairedWiFiController) stopLocked() {
	if c.runtime != nil {
		c.runtime.stop()
		c.runtime = nil
	}
}

func (c *pairedWiFiController) pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
}

// reconcile makes the running runtime match the current settings.
func (c *pairedWiFiController) reconcile() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
	if c.ctx == nil || c.ctx.Err() != nil {
		return
	}
	c.runtime = startPairedWiFiRuntime(c.ctx, c.store)
}

// paired reports the paired tablet's name while the runtime is running.
func (c *pairedWiFiController) paired() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtime == nil {
		return "", false
	}
	return c.runtime.Label, true
}
