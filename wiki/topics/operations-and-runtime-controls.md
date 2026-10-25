---
title: Operations and Runtime Controls
type: topic
tags: [operations, webui, autostart, diagnostics, release]
created: 2026-08-12
updated: 2026-08-12
sources:
  - README.md
  - BUILD.md
  - README_PUBLIC.md
summary: Operational controls for running, configuring, observing, and shipping DrawSplay.
---

# Operations and Runtime Controls

## Runtime control planes

- CLI flags (`host` startup and behavior controls).
- Web UI (`http://127.0.0.1:27180`) for settings, status, diagnostics, session controls.
- Autostart service commands (`-service install|uninstall|status`).
- Windows tray icon (default on Windows: `-background` defaults to true; `-background=false` for a console run).
- Windows installer/uninstaller (release asset `drawsplay_installer_<version>.exe`, or `DrawSplay.exe -install`; `DrawSplay-Uninstall.exe` / `-uninstall`).

## Windows install model

- Elevation happens once, at install: binaries to `%ProgramFiles%\DrawSplay` (admin-only writable), Parsec VDD driver, ffmpeg runtime, VDD custom-resolution key ACL, a WinUSB package signed on the PC (no Zadig), Apps & Features entry, and on-demand scheduled tasks (highest privileges, no triggers): `\DrawSplay\Restart Virtual Display Adapter` for adapter restarts and `\DrawSplay\Bind Tablet USB Driver` (`-winusb-bind-attached`) to add newly attached tablet identities to the WinUSB package.
- Per-user state is in Roaming: `%APPDATA%\DrawSplay\` (`config.json`, `install.json`, `winusb-attempts.json`, `host.log`) plus Start Menu shortcuts and an optional desktop shortcut (`-desktop-shortcut`); HKCU Run autostart if chosen at install (`-autostart`/`-no-autostart`).
- An installed host (`install.json` present) never raises UAC at launch; it uses the scheduled tasks for adapter restarts and WinUSB binding. Security: the task runs only the Program Files binary with a fixed argument, so user-level code cannot redirect it.
- Uninstall: one UAC prompt; removes task, uninstall entry, install folder (incl. bundled ffmpeg), autostart, shortcuts and optionally `%APPDATA%\DrawSplay`. Dependencies: DrawSplay-generated WinUSB packages and their `CN=DrawSplay USB Driver Signing` certificates are always deleted; the Parsec VDD is removed silently only when DrawSplay installed it (`vdd-installed-by-drawsplay` marker) and the Parsec app is absent; shared components (pre-existing/Parsec-app VDD, Zadig libwdi AOA bindings) need confirmation or `-remove-shared`. Other vendors' drivers (Samsung, SuperDisplay) are never touched.

## Operational workflows

- Source/dev run path: build host + run Android app.
- Binary distribution path: `drawsplay_installer_<version>.exe` + `DrawSplay.APK`.
- Release packaging path: script + CI workflows.

## Observability and troubleshooting

- Host status and warnings in web UI.
- Diagnostics snapshot export.
- Build/test commands in [BUILD summary](../sources/build-summary.md).

