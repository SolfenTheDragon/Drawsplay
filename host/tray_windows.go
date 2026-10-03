//go:build windows

package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/systray"
)

//go:embed drawsplay-host.ico
var trayIcon []byte

type trayRuntimeController struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *trayRuntimeController) start(parent context.Context, store *settingsStore) bool {
	c.mu.Lock()
	if c.cancel != nil {
		c.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	c.cancel = cancel
	c.done = done
	c.mu.Unlock()

	go func() {
		defer close(done)
		if err := runHostRuntime(ctx, store, func() { c.stop() }); err != nil && ctx.Err() == nil {
			log.Printf("Host runtime stopped with error: %v", err)
		}
		c.mu.Lock()
		if c.done == done {
			c.cancel = nil
			c.done = nil
		}
		c.mu.Unlock()
	}()
	return true
}

func (c *trayRuntimeController) stop() bool {
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	c.mu.Unlock()
	if cancel == nil || done == nil {
		return false
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		log.Printf("Timed out waiting for host runtime shutdown")
	}
	return true
}

func (c *trayRuntimeController) running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancel != nil
}

func runBackgroundTrayController(background bool, parent context.Context, cancel context.CancelFunc, store *settingsStore) bool {
	if !background {
		return false
	}

	ctrl := &trayRuntimeController{}
	ctrl.start(parent, store)

	var (
		menuStatus *systray.MenuItem
		menuOpen   *systray.MenuItem
		menuStart  *systray.MenuItem
		menuStop   *systray.MenuItem
		menuExit   *systray.MenuItem
		onceExit   sync.Once
	)

	updateMenu := func() {
		running := ctrl.running()
		if running {
			menuStart.Disable()
			menuStop.Enable()
		} else {
			menuStart.Enable()
			menuStop.Disable()
		}
		statusLabel := trayConnectionStatusLabel(running, activeInputDevice())
		menuStatus.SetTitle(statusLabel)
	}

	exitAll := func() {
		onceExit.Do(func() {
			ctrl.stop()
			cancel()
			systray.Quit()
		})
	}

	go func() {
		<-parent.Done()
		exitAll()
	}()

	onReady := func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("DrawSplay")
		systray.SetTooltip("DrawSplay")

		menuStatus = systray.AddMenuItem("🔴 Disconnected", "Tablet connection status")
		menuStatus.Disable()
		systray.AddSeparator()
		menuOpen = systray.AddMenuItem("Open Settings", "Open DrawSplay settings")
		systray.AddSeparator()
		menuStart = systray.AddMenuItem("Start", "Start DrawSplay host runtime")
		menuStop = systray.AddMenuItem("Stop", "Stop DrawSplay host runtime")
		systray.AddSeparator()
		menuExit = systray.AddMenuItem("Exit", "Exit DrawSplay")
		updateMenu()

		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-menuOpen.ClickedCh:
					openSettingsWebUI()
				case <-menuStart.ClickedCh:
					if ctrl.start(parent, store) {
						log.Printf("Host runtime started from tray")
					}
					updateMenu()
				case <-menuStop.ClickedCh:
					if ctrl.stop() {
						log.Printf("Host runtime stopped from tray")
					}
					updateMenu()
				case <-menuExit.ClickedCh:
					exitAll()
					return
				case <-ticker.C:
					updateMenu()
				case <-parent.Done():
					return
				}
			}
		}()
	}

	onExit := func() {
		ctrl.stop()
	}

	systray.Run(onReady, onExit)
	return true
}

func openSettingsWebUI() {
	url := fmt.Sprintf("http://127.0.0.1:%d", webuiPort)
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start(); err != nil {
		log.Printf("Open Web UI failed: %v", err)
	}
}

func trayConnectionStatusLabel(running bool, device string) string {
	if !running {
		return "🔴 Disconnected"
	}
	device = normalizeTrayDeviceName(device)
	if device == "" {
		return "🔴 Disconnected"
	}
	return "🟢 Connected: " + device
}

func normalizeTrayDeviceName(device string) string {
	device = strings.TrimSpace(device)
	device = strings.ReplaceAll(device, "\r", " ")
	device = strings.ReplaceAll(device, "\n", " ")
	device = strings.ReplaceAll(device, "\t", " ")
	device = strings.TrimSpace(device)
	if device == "" || device == "unknown-tablet" || device == "unknown-client" {
		return ""
	}
	return device
}
