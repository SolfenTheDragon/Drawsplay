// Command host is the DrawSplay host: it streams this computer's desktop to
// the DrawSplay tablet app and injects the tablet's touch, pen and keyboard
// input. This comment explains how a connection flows through the program and
// which file does what. Behaviour is specified in ../FUNCTIONALITY.md.
//
// Platform code uses file suffixes: _linux.go, _windows.go, and _other.go or
// _nonwindows.go for the rest. When a function is declared in several such
// files, read the one for your platform.
//
// # Start-up
//
// main.go parses flags (settings_flags.go lists those that override saved
// settings) and merges them over config.json (config.go): explicit flags win
// over the file, which wins over defaults. runtime_host.go takes the
// single-instance lock, starts everything below and waits for shutdown. On
// exit, releaseVirtualDisplaysOnExit removes any virtual display still held.
//
// # Transports
//
// Every transport hands two net.Conns (input and video) to the same handlers.
//
//   - USB accessory (AOA), the default: aoa_transport.go (polling, switching
//     the tablet), aoa_protocol.go (accessory hand-shake), aoa_link.go (5-byte
//     frames carrying virtual connections), aoa_linux.go / aoa_windows.go.
//   - ADB reverse tunnel (opt-in): adb.go, adb_loopback.go, loopback_peer_*.go.
//   - Paired Wi-Fi (beta): wifi_*.go and internal/wifilan (discovery,
//     pairing, mutual TLS, frames, session set-up).
//
// session.go decides which transport owns the session when several connect.
//
// # Input
//
// input.go reads mux frames (usbmux.go), records the tablet's hello as a
// clientSession (client_session.go: device, identity, codecs, features) and
// passes input and key packets to the injector (inject.go, inject_linux.go,
// inject_touch_linux.go, inject_windows.go, keymap.go). Display defaults
// derived from the identity are in client_identity.go, calibration in
// calibration.go.
//
// # Video
//
// video.go runs one session at a time:
//
//  1. Wait for the tablet's hello and copy its clientSession into the
//     session's videoOptions, then create a display (display.go).
//     Linux (display_linux.go chooses): X11 (x11_extend_linux.go), GNOME
//     (mutter_extend_linux.go), sway, Hyprland or KWin (wl_extend_linux.go
//     with sway_extend_linux.go, hypr_extend_linux.go, kwin_extend_linux.go),
//     or a mirror. The three extend backends share the hold-and-release
//     life cycle in held_display_linux.go. Windows: display_windows.go with
//     the IddCx driver
//     (idd_*.go), Parsec (parsec_*.go) or a mirror.
//  2. Build ranked capture and encode pipelines (pipeline.go,
//     pipeline_linux.go, pipeline_windows.go, native_media*.go) and run one
//     (ffmpeg.go).
//  3. Split the encoder output into H.264 units (internal/h264,
//     au_boundary.go) and send them as video packets (protocol.go). On Wi-Fi,
//     wifi_video_bridge.go turns those packets into Wi-Fi frames.
//  4. Tablet feedback (feedback.go) drives the bitrate ladder (ratecontrol.go,
//     congestion*.go) and temporal-layer dropping (temporal.go).
//
// # Settings page, status and tray
//
// webui_*.go and webui/ serve http://127.0.0.1:27180; webui_security.go holds
// the CSRF and Host-header checks. host_snapshot.go builds the status and
// diagnostics responses from the status cells (latest.go) the rest of the
// host publishes. Form controls bound to a setting carry
// data-setting attributes (webui/index.html). The trays are tray_linux.go and
// tray_windows.go, with shared labels in tray_status.go.
//
// # Installing and tools
//
// Windows install and uninstall: install_*.go, uninstall_deps_windows.go,
// idd_install_windows.go, winusb_sign_windows.go, idd_sign_windows.go,
// driver_stage_windows.go. Finding and downloading ffmpeg and adb: tools.go,
// tools_bootstrap_*.go. Autostart: service*.go.
//
// # Shared helpers
//
// fsutil.go (atomic file writes, path containment, hashing) and resilience.go
// (panic logging, throttled logs).
package main
