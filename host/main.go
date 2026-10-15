// Drawsplay host service.
//
// The host exposes two localhost TCP listeners that client apps reach
// over USB via `adb reverse`:
//
//	:27183  input channel  â€” newline-delimited JSON from the tablet
//	                         (see protocol.go / FUNCTIONALITY.md Â§3)
//	:27184  video channel  â€” length-prefixed H.264 NAL units to the tablet
//	                         (see protocol.go and video.go)
//
// File map for contributors:
//
//	main.go     â€” flags, startup wiring, shutdown handling (this file)
//	config.go   â€” persisted settings + runtime settings store (web UI backend)
//	webui_*.go  â€” localhost settings frontend (embedded page + JSON API)
//	service.go  â€” login autostart management (+ service_windows.go /
//	              service_linux.go platform implementations)
//	protocol.go â€” wire formats shared by both channels
//	adb.go      â€” adb availability check + reverse-tunnel maintenance
//	tools.go    â€” adb/ffmpeg discovery in well-known install locations
//	resilience.go â€” session panic containment, duplicate-instance detection
//	input.go    â€” input channel listener and packet handling
//	inject.go   â€” input injection abstraction (+ inject_windows.go /
//	              inject_linux.go platform implementations)
//	eventlog.go â€” bounded async diagnostic logger for the input path
//	video.go    â€” video channel listener, session lifecycle, NAL framing
//	feedback.go â€” client stats reader, congestion + dead-peer signals
//	ratecontrol.go â€” adaptive bitrate ladder
//	keymap.go   â€” Android keycode â†’ VK / evdev table (keyboard passthrough)
//	display.go  â€” virtual display abstraction (+ display_windows.go /
//	              display_linux.go platform implementations)
//	vddinstall_windows.go â€” automatic Parsec VDD driver install
//	pipeline.go â€” capture pipeline specs (+ pipeline_windows.go /
//	              pipeline_linux.go platform builders)
//	ffmpeg.go   â€” capture/encode subprocess supervision, encoder detection
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	adb := flag.String("adb", "adb", "ADB executable path")
	adbSerial := flag.String("adb-serial", "", "preferred ADB device serial (optional; reverse only this device)")
	requireADBSerial := flag.Bool("require-adb-serial", false, "require preferred ADB serial and refuse unapproved devices")
	autoLaunchAndroidApp := flag.Bool("auto-launch-android-app", false, "launch the DrawSplay Android app automatically on eligible tablet connect")
	zeroCopyCapture := flag.Bool("zero-copy-capture", false, "try zero-copy NVENC capture path first on Windows (falls back automatically)")
	temporalLayering := flag.Bool("temporal-layering", false, "enable temporal enhancement-layer probe and runtime non-reference frame adaptation when supported")
	wifiEnabled := flag.Bool("wifi-enabled", false, "enable optional Wi-Fi transport listeners and LAN discovery")
	wifiBindAddress := flag.String("wifi-bind-address", "0.0.0.0", "Wi-Fi listener bind address")
	wifiInputPort := flag.Int("wifi-input-port", 37183, "Wi-Fi input channel port")
	wifiVideoPort := flag.Int("wifi-video-port", 37184, "Wi-Fi video channel port")
	wifiServiceName := flag.String("wifi-service-name", "DrawSplay", "Wi-Fi discovery service instance name")
	wifiTLS := flag.Bool("wifi-tls", true, "require TLS for Wi-Fi transport")
	wifiAuthToken := flag.String("wifi-auth-token", "", "optional Wi-Fi auth token required in hello packet")
	wifiMaxBitrate := flag.String("wifi-max-bitrate", "12M", "Wi-Fi session max bitrate cap")
	usbLinkEnabled := flag.Bool("usb-link-enabled", false, "enable USB virtual-NIC TCP transport listeners")
	usbLinkBindAddress := flag.String("usb-link-bind-address", "0.0.0.0", "USB Link listener bind address")
	usbLinkInputPort := flag.Int("usb-link-input-port", 37193, "USB Link input channel port")
	usbLinkVideoPort := flag.Int("usb-link-video-port", 37194, "USB Link video channel port")
	aoaEnabled := flag.Bool("aoa-enabled", true, "ADB-free USB transport over Android Open Accessory (no developer mode needed)")
	adbTunnel := flag.Bool("adb-tunnel", false, "enable the adb reverse tunnel transport (needs USB debugging on the tablet)")
	usbLinkTLS := flag.Bool("usb-link-tls", true, "enable TLS for USB Link transport")
	usbLinkAuthToken := flag.String("usb-link-auth-token", "", "required USB Link auth token")
	usbLinkMaxBitrate := flag.String("usb-link-max-bitrate", "60M", "USB Link session max bitrate cap")
	ffmpeg := flag.String("ffmpeg", "ffmpeg", "ffmpeg executable path (used for screen capture/encode)")
	allowToolAutoInstall := flag.Bool("allow-tool-auto-install", true, "allow automatic runtime adb/ffmpeg download when missing (Windows)")
	fps := flag.Int("fps", 120, "capture frame rate")
	bitrate := flag.String("bitrate", "24M", "video bitrate at 1080p (ffmpeg syntax, e.g. 24M); scaled by display pixel count and capped at the USB 2.0 link budget")
	width := flag.Int("width", 1920, "virtual display width (extend mode)")
	height := flag.Int("height", 1080, "virtual display height (extend mode)")
	noDriverInstall := flag.Bool("no-driver-install", false, "skip automatic virtual display driver installation (Windows)")
	keepAliveOnBackground := flag.Bool("keep-alive-on-background", false, "keep tablet session alive while the Android app is backgrounded")
	vddCustomResolutionAdminSetup := flag.Bool("vdd-custom-resolution-admin-setup", false, "internal: run elevated VDD custom-resolution setup and exit")
	vddCustomResolutionWidth := flag.Int("vdd-custom-resolution-width", 0, "internal: VDD custom-resolution width for elevated setup")
	vddCustomResolutionHeight := flag.Int("vdd-custom-resolution-height", 0, "internal: VDD custom-resolution height for elevated setup")
	vddRestartAdapter := flag.Bool("vdd-restart-adapter", false, "internal: restart the Parsec VDD adapter and exit")
	service := flag.String("service", "", "manage login autostart: install | uninstall | status")
	background := flag.Bool("background", false, "run without a console (used by the login autostart entry)")
	flag.Parse()

	if runVDDAdminMode(*vddCustomResolutionAdminSetup, *vddCustomResolutionWidth, *vddCustomResolutionHeight, *vddRestartAdapter) {
		return
	}

	if *service != "" {
		os.Exit(runServiceCommand(*service))
	}
	if *background {
		// May respawn the process hidden and exit (Windows); the hidden
		// child redirects logging to a file. No-op on Linux (journald).
		prepareBackground()
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	installDebugLogCapture()
	log.Println("Starting Drawsplay host")
	if !wifiFeatureEnabled() {
		log.Printf("Wi-Fi feature is disabled (set %s=1 to enable)", wifiFeatureFlagEnv)
	}

	// Settings precedence: explicit CLI flags > config file (web UI) >
	// built-in defaults. Flags left at their default follow the config.
	cfg := loadSettings()
	overrides := map[string]func(){
		"adb":                      func() { cfg.ADB = *adb },
		"adb-serial":               func() { cfg.PreferredADBSerial = *adbSerial },
		"require-adb-serial":       func() { cfg.RequirePreferredADB = *requireADBSerial },
		"auto-launch-android-app":  func() { cfg.AutoLaunchAndroidApp = *autoLaunchAndroidApp },
		"zero-copy-capture":        func() { cfg.ZeroCopyCapture = *zeroCopyCapture },
		"temporal-layering":        func() { cfg.TemporalLayering = *temporalLayering },
		"wifi-enabled":             func() { cfg.WifiEnabled = *wifiEnabled },
		"wifi-bind-address":        func() { cfg.WifiBindAddress = *wifiBindAddress },
		"wifi-input-port":          func() { cfg.WifiInputPort = *wifiInputPort },
		"wifi-video-port":          func() { cfg.WifiVideoPort = *wifiVideoPort },
		"wifi-service-name":        func() { cfg.WifiServiceName = *wifiServiceName },
		"wifi-tls":                 func() { cfg.WifiTLS = *wifiTLS },
		"wifi-auth-token":          func() { cfg.WifiAuthToken = *wifiAuthToken },
		"wifi-max-bitrate":         func() { cfg.WifiMaxBitrate = *wifiMaxBitrate },
		"aoa-enabled":              func() { cfg.AOAEnabled = *aoaEnabled },
		"adb-tunnel":               func() { cfg.ADBTunnelEnabled = *adbTunnel },
		"usb-link-enabled":         func() { cfg.USBLinkEnabled = *usbLinkEnabled },
		"usb-link-bind-address":    func() { cfg.USBLinkBindAddress = *usbLinkBindAddress },
		"usb-link-input-port":      func() { cfg.USBLinkInputPort = *usbLinkInputPort },
		"usb-link-video-port":      func() { cfg.USBLinkVideoPort = *usbLinkVideoPort },
		"usb-link-tls":             func() { cfg.USBLinkTLS = *usbLinkTLS },
		"usb-link-auth-token":      func() { cfg.USBLinkAuthToken = *usbLinkAuthToken },
		"usb-link-max-bitrate":     func() { cfg.USBLinkMaxBitrate = *usbLinkMaxBitrate },
		"ffmpeg":                   func() { cfg.FFmpeg = *ffmpeg },
		"allow-tool-auto-install":  func() { cfg.AllowToolAutoInstall = *allowToolAutoInstall },
		"fps":                      func() { cfg.FPS = *fps },
		"bitrate":                  func() { cfg.Bitrate = *bitrate },
		"width":                    func() { cfg.Width = *width },
		"height":                   func() { cfg.Height = *height },
		"no-driver-install":        func() { cfg.NoDriverInstall = *noDriverInstall },
		"keep-alive-on-background": func() { cfg.KeepAliveOnBackground = *keepAliveOnBackground },
	}
	flag.Visit(func(f *flag.Flag) {
		if apply, ok := overrides[f.Name]; ok {
			apply()
		}
	})
	cfg = applyFeatureFlags(cfg)
	if err := cfg.validate(); err != nil {
		log.Fatalf("Invalid settings: %v", err)
	}
	// Resolve tools not on PATH from well-known install locations.
	if cfg.ADBTunnelEnabled {
		cfg.ADB = resolveTool(cfg.ADB, "adb", cfg.AllowToolAutoInstall)
	}
	cfg.FFmpeg = resolveTool(cfg.FFmpeg, "ffmpeg", cfg.AllowToolAutoInstall)
	if err := cfg.validate(); err != nil {
		log.Fatalf("Resolved tool settings are invalid: %v", err)
	}
	store := newSettingsStore(cfg)
	globalSettingsStore = store

	// Must precede any display enumeration or input injection: without DPI
	// awareness, Windows virtualizes coordinates on scaled displays and
	// injected input lands in the wrong place.
	declareDPIAware()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Ctrl+C / SIGTERM triggers a coordinated shutdown of all listeners.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		cancel()
	}()

	if runBackgroundTrayController(*background, ctx, cancel, store) {
		releaseVirtualDisplaysOnExit()
		log.Println("Shutting down")
		return
	}
	err := runHostRuntime(ctx, store, cancel)
	releaseVirtualDisplaysOnExit()
	if err != nil {
		log.Fatalf("%v", err)
	}
	log.Println("Shutting down")
}
