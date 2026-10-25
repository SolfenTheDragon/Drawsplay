---
title: Display Backends and Host Runtime Details
type: topic
tags: [linux, x11, wayland, gnome, kde, sway, hyprland, windows, iddcx, transport, aoa, adb, privacy]
created: 2026-10-15
updated: 2026-10-16
sources:
  - README.md (pre-user-focused rewrite and later adb removal)
summary: Implementation-level details removed from the user-facing README: first-run behavior, per-desktop virtual display backends, transport modes, and resolution control per platform.
---

# Display Backends and Host Runtime Details

The public `README.md` is user-focused (functionality, install, configuration, troubleshooting). This page keeps the implementation detail that used to live there.

## First-run behavior (Windows host)

1. The host starts local listeners for input and video.
2. The host switches a connected tablet into USB accessory (AOA) mode, installing its signed WinUSB driver if needed; `adb reverse` mappings are maintained only when the ADB tunnel is enabled.
3. If settings still use the defaults (`adb`/`ffmpeg`), tool resolution is automatic: prefer bundled copies under `runtime\`, otherwise PATH / known install locations, otherwise (ffmpeg only) auto-download the newest OS/arch-compatible ffmpeg into `runtime\ffmpeg\`. adb is never auto-downloaded.
4. If DrawSplay's bundled IddCx virtual display driver is missing and auto-install is enabled, the portable host may attempt setup (UAC prompt). Setup signs/installs DrawSplay first; only if unavailable does it reuse Parsec VDD or download the pinned, publisher-verified 0.41 fallback when absent and permitted. A working DrawSplay driver means no Parsec download/install. An installed host defers repairs to setup and never elevates on launch.

Optional prebundled runtime files for offline setup: `runtime\adb\adb.exe`, `runtime\ffmpeg\ffmpeg.exe` (plus ffmpeg DLLs).

## Linux launcher and installer behavior

- `install-host-linux.sh` runs as the normal user (not sudo). On Ubuntu/Debian it installs missing tools and sets up USB/input permissions with sudo prompts. `--no-install-deps` keeps system changes manual. Automatic package installation requires `apt-get`.
- `install-drawsplay-host-linux.sh` copies the host into `~/.local/bin`, installs launchers and icon under `~/.local/share`, and enables start at login. `uninstall-drawsplay-host-linux.sh` removes them but keeps settings and logs.
- Manual Ubuntu setup: `android-sdk-platform-tools android-sdk-platform-tools-common ffmpeg`; a udev rule `KERNEL=="uinput", GROUP="input", MODE="0660"`; user in `input,plugdev`.
- Wayland PipeWire capture needs `gstreamer1.0-tools`, `-plugins-good`, `-plugins-bad`, `-plugins-ugly`, `gstreamer1.0-pipewire`. The X11 backup needs `x11-xserver-utils` (xrandr).
- Hardware encoding needs VA-API drivers (`intel-media-va-driver-non-free` for Intel, `mesa-va-drivers` for AMD, `vainfo`); otherwise software encoding is used.
- Pen and touch are mapped to the tablet's display by DrawSplay itself (uinput device `Drawsplay Pen`), or via Cinnamon/GNOME tablet settings when those run.

## Per-desktop virtual display backends

### Windows

Each USB or Wi-Fi video session tries DrawSplay IddCx first, then an existing
Parsec VDD, then primary-display mirroring. Parsec shares the same output
discovery, input mapping, refresh/recovery and removal lifecycle, with its own
50 ms driver keepalive. It uses only existing Parsec modes: no custom-mode
registry writes or shared-adapter restarts. Exact tablet native size/refresh
is not guaranteed on that fallback. Repeated capture failure still activates
the mirror cooldown rather than repeatedly recreating virtual monitors.
Setup records Parsec ownership separately and preserves pre-existing/shared
or ambiguously owned legacy drivers unless explicitly authorized to remove them.

Wayland is the preferred Linux session; X11 is the backup.

### X11 extended display (vkms)

X11 cannot create virtual monitors, so DrawSplay uses an output from the `vkms` kernel module (`sudo modprobe vkms`, included in Ubuntu kernels).

- The host loads vkms itself at start and on each tablet connection when the X11 session has no virtual output. At host start it never prompts (root or passwordless sudo only). When a tablet connects it may show one polkit (pkexec) password prompt that also configures vkms to load at boot, so the prompt is needed once; a dismissed prompt is not repeated for two minutes.
- The launcher offers to load the module and configure boot loading (`echo vkms | sudo tee /etc/modules-load.d/drawsplay-vkms.conf`). Loading during an X11 session reconfigures the X server's displays, so save work first.
- Each connection switches `Virtual-1-1` on at the configured resolution, right of other monitors; only that display is streamed. If the output appears after a session started in mirror mode, the stream restarts in extended mode automatically.
- Resolution/arrangement are changed in the desktop's display settings (Cinnamon: System Settings > Display); DrawSplay follows and remembers.
- While the computer is locked the stream goes black and resumes after unlock.
- The virtual display is switched off a few seconds after disconnect and on host exit; if it was primary or the only display, the physical monitor is made primary (and re-enabled).
- Use another output (for example a DisplayLink adapter) by setting `DRAWSPLAY_X11_OUTPUT` to its xrandr name before starting the host.

### GNOME Wayland (Mutter ScreenCast)

- GNOME creates the monitor; no kernel module. Needs the PipeWire capture packages.
- Each connection adds a virtual monitor right of the others; only it is streamed. DrawSplay preserves the user's arrangement (scale, primary, underscanning) instead of GNOME's reflow, and restores the monitor's last position on the next connection. It never saves a display configuration, so the Settings arrangement is unchanged.
- Resolution is changed in Settings > Displays (standard resolutions plus the tablet's native one).
- The lock screen blacks the stream; it resumes after unlock. The monitor is removed a few seconds after disconnect and on exit.
- The tray icon works under Wayland; stock GNOME needs the AppIndicator extension (Ubuntu includes it).
- Differences from X11: do not switch the virtual monitor off in Settings > Displays (crashes GNOME Shell 50); stopping the share from the top-bar indicator removes the monitor (DrawSplay adds a new one); rotation unsupported (streamed unrotated); moving the monitor does not interrupt the stream.

### KDE Plasma 6, sway, Hyprland

Each connection creates a virtual display at the configured resolution, right of other monitors (or where last placed); only it is streamed. Input is mapped automatically; resolution changes are followed and remembered. A reconnect within a few seconds keeps the display; it is removed a few seconds after disconnect and on exit.

- **KDE Plasma 6 (KWin):** needs PipeWire capture packages. On first use the host installs `~/.local/share/applications/drawsplay-host.desktop` so it can use KWin's screencast protocol (reconnect if the display does not appear first time). KWin remembers layout of `Virtual-Drawsplay` (System Settings > Display & Monitor). Resolution is fixed while it exists, so change it in DrawSplay's settings panel.
- **sway:** 1.9+ removes the display completely, older versions switch it off. Arrange with `swaymsg output HEADLESS-N ...` or wdisplays. Other displays move so the top-left stays 0,0.
- **Hyprland:** display is named `Drawsplay`; arrange with `hyprctl keyword monitor Drawsplay,...` (or `hl.monitor` in Lua config). Other monitor rules are untouched.
- The display is set up at runtime, so a config reload (`swaymsg reload`, saving `hyprland.conf`) resets it to the config's rules; DrawSplay restarts the stream without saving that resolution. Config rules for the virtual display are overridden on each connection.

### Other Wayland desktops

For example Cinnamon on Wayland (experimental): no virtual display can be created. The tablet mirrors XWayland's screen, so only X11 apps show (native Wayland windows are black) and the desktop is not extended. The settings panel shows a warning and a "Wayland virtual display" checklist item. Use an X11 session with vkms, or GNOME/KDE/sway/Hyprland.

## Connection and transports

- Default path: the app connects over USB; the host exposes localhost listeners (input 27183, video 27184, settings UI 27180) reached via `adb reverse` or USB accessory mode. No manual IP entry or pairing.
- USB accessory mode (AOA) is on by default (`aoaEnabled`, flag `-aoa-enabled`) and needs no Developer options. The older adb tunnel is off by default; enable *ADB tunnel* in Advanced settings or start with `-adb-tunnel` (this also reveals the adb options). On Linux copy `packaging/linux/70-drawsplay-aoa.rules` to `/etc/udev/rules.d/` and reload udev; on Windows the installer/host installs a self-signed WinUSB package for the accessory device (no Zadig; see `packaging/windows/README.md`). Switching the tablet into accessory mode ends any adb session on it.
- With Wi-Fi transport enabled, the tablet keeps known-host trust from host policy sync so later launches reconnect over Wi-Fi without a new code.
- The tablet waiting screen shows **Forget wireless host** when a saved paired
  host record exists (including corrupt or incomplete records). It stops paired
  reconnect and closes pending/active sockets before deleting saved pairing
  files off the main thread. Then **Wi-Fi connect** can pair another host.
  Failure is shown with retry; USB, device identity and display preferences
  are kept. This does not remotely delete the tablet from the old host.
- See also [Transport and Session Model](transport-and-session-model.md) and [AOA Transport Implementation Research](aoa-transport-implementation-research.md).

### Tablet connection UX

The Android waiting screen gives USB accessory instructions and offers an
explicit retry after permission refusal/open failure. Saved Wi-Fi reconnect,
no usable Wi-Fi, USB priority, unreadable pairing and unavailable Keystore
identity are separate states, not guesses based on exception text. Check the
host's Wi-Fi toggle/interface and network before repairing trust. Forgetting
requires confirmation, is local-only and does not rotate tablet keys.

Pairing shows ordered computer-side instructions and a persistent eight-digit
code label. Code verification is followed by an explicit computer approval
wait; failure provides a fresh-attempt **Try again**, with old callbacks
invalidated. Discovery/socket setup is off the UI thread.

Setup panels scroll and are capped at a readable width; only the local overlay
receives system/cutout/keyboard insets. Remote video and input retain identical
geometry and fullscreen behavior. Android navigation is visible during setup;
while displaying, reveal navigation with an edge swipe and use **Back** for
**Connection options**. Back uses AndroidX dispatch, not the deprecated activity
override. Physical-key forwarding is unchanged. No permanent drawing toolbar
is added. The waiting overlay is not dismissed by configuration alone:
first-picture readiness is required.

## Resolution and refresh control per platform

- **Windows with the Android app:** the tablet reports its panel refresh rates in `hello` (`screenRefreshRates`). DrawSplay's bundled IddCx driver creates the virtual monitor at the tablet's native resolution and the highest reported refresh rate (e.g. 2960x1848 @ 120 Hz) — its only advertised mode — so the display defaults to the tablet's refresh automatically, with no custom-resolution registry key or adapter reload. Capture fps follows the live Windows display refresh, capped at the panel rate (default 120 for apps that do not report it).
- **GNOME Wayland:** Settings > Displays. **X11 extended:** desktop display settings (`Virtual-1-1`). **sway/Hyprland:** output commands. **KDE Plasma:** DrawSplay's settings panel. Frame rate stays in DrawSplay's settings panel (Linux is capped at 60).
- Video-setting changes restart the active stream automatically.
- Web settings panel (`http://127.0.0.1:27180`): bitrate, input toggles, tool paths, session controls (disconnect, temporary input lock), diagnostics snapshot download, first-run checklist and health counters, setup repair (restarts ADB checks), and video reconnect.

## ADB behavior (advanced, removed from the README)

ADB is an optional, advanced path and is **not** a dependency of the default (AOA) connection. Only users of the adb tunnel need it, and they are expected to install it themselves.

- The adb reverse tunnel is off by default (`adbTunnelEnabled`, flag `-adb-tunnel`). Enable *ADB tunnel* in Advanced settings on the web UI (this reveals the adb options) or start the host with `-adb-tunnel`. It needs Developer options and USB debugging on the tablet. While enabled, the host keeps `adb reverse` mappings alive per device serial.
- Switching the tablet into accessory mode ends any adb session on it.
- Tool resolution (Windows): the `adb` path setting defaults to `adb`; the host prefers a bundled `runtime\adb\adb.exe`, then PATH / known locations; it is never auto-downloaded (only needed for the opt-in legacy ADB transport). ffmpeg: the installer always installs the newest compatible release (gyan.dev essentials via `GyanD/codexffmpeg`, BtbN stable as fallback/ARM64), SHA-256-verified against the GitHub release digest, with a `drawsplay-runtime.json` manifest of per-file hashes the host checks before use. The README no longer lists adb as a dependency; the Linux launcher still installs `android-sdk-platform-tools` on Ubuntu/Debian.
- Sideloading the APK with adb (only while the tablet is not in accessory mode; ADB cannot see it while bound to WinUSB): `adb install -r DrawSplay.APK` (Windows `C:\DrawSplay\DrawSplay.APK`). The README now tells users to copy the APK to the tablet and open it instead.
- Troubleshooting: tablet not listed by ADB: `adb kill-server`, `adb start-server`, `adb devices`, reconnect and accept the USB debugging prompt. Windows with no global adb: install platform-tools yourself (adb is not auto-downloaded). Linux `no permissions` in `adb devices`: check Android USB udev rules and `plugdev`.
- Privacy: keep USB debugging enabled only on trusted host PCs; host listeners bind to localhost and rely on `adb reverse`.
- Setup repair in the web UI restarts the ADB checks.

## Privacy and security notes

- Host listeners bind to localhost; the settings page is localhost-only.
- Keep USB debugging enabled only on trusted hosts.
- See [Security Model](security-model.md).

## Extra troubleshooting detail

- Windows ffmpeg check: `"C:\Program Files\DrawSplay\runtime\ffmpeg\ffmpeg.exe" -version` (adb is not auto-downloaded; install platform-tools yourself if you need the legacy ADB transport).
- Linux `adb devices` shows `no permissions`: check the Android USB udev rules and `plugdev` membership. Pen/touch not working: check the `/dev/uinput` rule and `input` group; log out/in or restart.
- Two cursors after switching between mouse and pen on Linux is a known GNOME/Mutter limitation. On extended displays the host log prints `Mapped Drawsplay Pen ...`; if touch lands on another monitor, check the tablet's *Map to Monitor* setting in Cinnamon/GNOME tablet settings.
- X11 multi-touch: DrawSplay delivers up to 10 touches, but some apps need enabling (Firefox: `MOZ_USE_XINPUT2=1 firefox`, or prefix the `Exec=` lines of a copied `.desktop` file with `env MOZ_USE_XINPUT2=1`). GIMP has no canvas pinch/two-finger gestures; Krita, Xournal++ and Inkscape do.
- Mirror instead of extended on Linux: the host log gives the reason (`PipeWire capture tooling unavailable ...`, `Mutter virtual monitor unavailable ...`, `X11 extended display unavailable ...`). If vkms is loaded but `xrandr` shows no `Virtual-1-1`, log out and back in.
- Windows virtual display driver did not complete: rerun setup and approve UAC; reboot if requested. Runtime tries existing DrawSplay and Parsec drivers before mirroring. Setup downloads Parsec only after the preferred driver fails and no Parsec installation exists; repair an existing unavailable Parsec driver manually.
