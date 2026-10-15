package main

import (
	"context"
	"log"
	"time"
)

const (
	aoaScanInterval  = 2 * time.Second
	aoaSwitchBackoff = 15 * time.Second
	aoaLinkRetryWait = 2 * time.Second
)

// startAOATransport serves a tablet over Android Open Accessory when enabled.
// It needs no ADB and no Developer options on the tablet. It is additive: the
// adb-reverse path keeps working and shares the "usb" session ownership.
func startAOATransport(ctx context.Context, store *settingsStore) {
	if !store.get().AOAEnabled {
		return
	}
	log.Printf("USB accessory (AOA) transport enabled")
	go runAOATransport(ctx, store, aoaScanDevices)
}

type aoaScanFunc func() ([]aoaCandidate, error)

func runAOATransport(ctx context.Context, store *settingsStore, scan aoaScanFunc) {
	inputLn := newAOAListener()
	go func() {
		<-ctx.Done()
		_ = inputLn.Close()
	}()
	go runInputListener(ctx, inputLn, store, "usb")

	onOpen := func(kind byte, conn *aoaConn) {
		switch kind {
		case aoaKindInput:
			if !inputLn.offer(conn) {
				_ = conn.Close()
			}
		case aoaKindVideo:
			go handleAcceptedVideoConnection(ctx, conn, store, "usb")
		default:
			_ = conn.Close()
		}
	}

	lastSwitch := map[string]time.Time{}
	ticker := time.NewTicker(aoaScanInterval)
	defer ticker.Stop()
	for {
		if aoaPollOnce(ctx, scan, lastSwitch, onOpen) {
			// A link just ended; give the tablet a moment before probing again.
			select {
			case <-ctx.Done():
				return
			case <-time.After(aoaLinkRetryWait):
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// aoaPollOnce scans for devices, switching Android devices into accessory mode
// and serving one accessory link. It reports whether a link was served.
func aoaPollOnce(ctx context.Context, scan aoaScanFunc, lastSwitch map[string]time.Time, onOpen func(byte, *aoaConn)) bool {
	cands, err := scan()
	if err != nil {
		throttledLog("aoa-scan", "AOA device scan failed: %v", err)
		return false
	}
	live := map[string]bool{}
	for _, c := range cands {
		live[c.Key] = true
	}
	for k := range lastSwitch {
		if !live[k] {
			delete(lastSwitch, k)
		}
	}

	for _, c := range cands {
		if !c.inAccessoryMode() {
			continue
		}
		dev, err := c.Open()
		if err != nil {
			throttledLog("aoa-open", "AOA: cannot open accessory device %04x:%04x: %v", c.VID, c.PID, err)
			continue
		}
		pipe, err := dev.OpenPipe()
		if err != nil {
			_ = dev.Close()
			throttledLog("aoa-pipe", "AOA: cannot open accessory pipe: %v", err)
			continue
		}
		log.Printf("AOA: accessory %04x:%04x open, waiting for the tablet app", c.VID, c.PID)
		link := newAOALink(pipe)
		err = link.serve(ctx, onOpen)
		log.Printf("AOA: link closed: %v", err)
		return true
	}

	for _, c := range cands {
		if c.inAccessoryMode() {
			continue
		}
		if t, ok := lastSwitch[c.Key]; ok && time.Since(t) < aoaSwitchBackoff {
			continue
		}
		lastSwitch[c.Key] = time.Now()
		dev, err := c.Open()
		if err != nil {
			throttledLog("aoa-open-"+c.Key, "AOA: cannot open %04x:%04x: %v", c.VID, c.PID, err)
			continue
		}
		err = aoaSwitchToAccessory(dev)
		_ = dev.Close()
		if err != nil {
			throttledLog("aoa-switch-"+c.Key, "AOA: %04x:%04x not switched to accessory mode: %v", c.VID, c.PID, err)
			continue
		}
		log.Printf("AOA: switched %04x:%04x to accessory mode; waiting for re-enumeration", c.VID, c.PID)
	}
	return false
}
