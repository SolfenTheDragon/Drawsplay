// Drawsplay host service.
//
// The host exposes two localhost TCP listeners that client apps reach
// over USB via AOA by default, or via the optional `adb reverse` transport:
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
	aoaEnabled := flag.Bool("aoa-enabled", true, "ADB-free USB transport over Android Open Accessory (no developer mode needed)")
	aoaAutoSwitch := flag.Bool("aoa-auto-switch", true, "automatically switch an attached tablet to accessory mode (otherwise use the web UI button)")
	adbTunnel := flag.Bool("adb-tunnel", false, "enable the adb reverse tunnel transport (needs USB debugging on the tablet)")
	ffmpeg := flag.String("ffmpeg", "ffmpeg", "ffmpeg executable path (used for screen capture/encode)")
	allowToolAutoInstall := flag.Bool("allow-tool-auto-install", true, "allow automatic download of the newest ffmpeg when missing (Windows)")
	fps := flag.Int("fps", 120, "capture frame rate")
	bitrate := flag.String("bitrate", "24M", "video bitrate at 1080p (ffmpeg syntax, e.g. 24M); scaled by display pixel count and capped at the USB 2.0 link budget")
	width := flag.Int("width", 1920, "virtual display width (extend mode)")
	height := flag.Int("height", 1080, "virtual display height (extend mode)")
	noDriverInstall := flag.Bool("no-driver-install", false, "skip automatic virtual display driver installation (Windows)")
	keepAliveOnBackground := flag.Bool("keep-alive-on-background", false, "keep tablet session alive while the Android app is backgrounded")
	driverInstallElevated := flag.Bool("driver-install-elevated", false, "internal: run elevated virtual display driver install and exit")
	service := flag.String("service", "", "manage login autostart: install | uninstall | status")
	background := flag.Bool("background", defaultBackgroundMode, "run without a console in the system tray (Windows default; use -background=false for a console run)")
	install := flag.Bool("install", false, "Windows: install DrawSplay for this PC (one administrator prompt), then start it in the tray")
	uninstall := flag.Bool("uninstall", false, "Windows: uninstall DrawSplay (also implied when run as DrawSplay-Uninstall.exe)")
	quiet := flag.Bool("quiet", false, "Windows install/uninstall: no confirmation or result dialogs")
	keepSettings := flag.Bool("keep-settings", false, "Windows uninstall: keep settings in %APPDATA%\\DrawSplay")
	removeShared := flag.Bool("remove-shared", false, "Windows uninstall with -quiet: also remove dependencies that may be shared with other software (Parsec virtual display driver, Zadig WinUSB bindings)")
	desktopShortcut := flag.Bool("desktop-shortcut", false, "Windows install: add a desktop shortcut without asking (with -quiet, an existing one is kept on reinstall)")
	winusbBind := flag.Bool("winusb-bind-attached", false, "internal: elevated WinUSB driver setup for attached tablets (Windows)")
	autostart := flag.Bool("autostart", false, "Windows install: start DrawSplay at sign-in without asking (default for a fresh -quiet install)")
	noAutostart := flag.Bool("no-autostart", false, "Windows install: do not start DrawSplay at sign-in (enable later in settings)")
	installElevated := flag.String("install-elevated", "", "internal: elevated install phase (result file path)")
	uninstallElevated := flag.String("uninstall-elevated", "", "internal: elevated uninstall phase (result file path)")
	flag.Parse()
	attachParentConsole()

	if handled, code := runInstallerMode(installerOptions{
		Install:           *install,
		Uninstall:         *uninstall,
		Quiet:             *quiet,
		KeepSettings:      *keepSettings,
		RemoveShared:      *removeShared,
		DesktopShortcut:   *desktopShortcut,
		Autostart:         *autostart,
		NoAutostart:       *noAutostart,
		SkipVDD:           *noDriverInstall,
		InstallElevated:   *installElevated,
		WinUSBBind:        *winusbBind,
		UninstallElevated: *uninstallElevated,
		NoArgs:            len(os.Args) == 1,
	}); handled {
		os.Exit(code)
	}

	if runDriverInstallAdminMode(*driverInstallElevated) {
		return
	}

	if *service != "" {
		os.Exit(runServiceCommand(*service))
	}
	if handOffToRunningInstance(*background) {
		// Already running in the tray: just open its settings page.
		return
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
		"aoa-auto-switch":          func() { cfg.AOAAutoSwitch = *aoaAutoSwitch },
		"adb-tunnel":               func() { cfg.ADBTunnelEnabled = *adbTunnel },
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
