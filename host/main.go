package main

// main parses flags, merges them over the saved settings and starts the
// runtime. doc.go explains how the host fits together.

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	fromFlags := defaultSettings()
	bindSettingFlags(flag.CommandLine, &fromFlags)
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
	uninstallRemoveVDD := flag.Bool("uninstall-remove-vdd", false, "internal: elevated uninstall phase also removes the Parsec virtual display driver")
	printVersion := flag.Bool("version", false, "print the host version and exit")
	uninstallRemoveZadig := flag.Bool("uninstall-remove-zadig", false, "internal: elevated uninstall phase also removes Zadig WinUSB bindings")
	flag.Parse()
	attachParentConsole()
	if *printVersion {
		fmt.Println(hostVersion())
		return
	}

	if handled, code := runInstallerMode(installerOptions{
		Install:           *install,
		Uninstall:         *uninstall,
		Quiet:             *quiet,
		KeepSettings:      *keepSettings,
		RemoveShared:      *removeShared,
		DesktopShortcut:   *desktopShortcut,
		Autostart:         *autostart,
		NoAutostart:       *noAutostart,
		SkipVDD:           fromFlags.NoDriverInstall,
		InstallElevated:   *installElevated,
		WinUSBBind:        *winusbBind,
		UninstallElevated: *uninstallElevated,
		UninstallRequest:  uninstallRequest{RemoveVDD: *uninstallRemoveVDD, RemoveZadig: *uninstallRemoveZadig},
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
		log.Printf("Wi-Fi feature is disabled (%s turned it off)", wifiFeatureFlagEnv)
	}

	// Settings precedence: explicit CLI flags > config file (web UI) >
	// built-in defaults. Flags left at their default follow the config.
	cfg := loadSettings()
	applySettingFlags(flag.CommandLine, &cfg)
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
		execPendingRestart()
		return
	}
	err := runHostRuntime(ctx, store, cancel, cancel)
	releaseVirtualDisplaysOnExit()
	if err != nil {
		log.Fatalf("%v", err)
	}
	log.Println("Shutting down")
	execPendingRestart()
}
