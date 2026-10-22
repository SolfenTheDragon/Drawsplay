---
title: Transport and Session Model
type: topic
tags: [transport, usb, wifi, usb-link, session]
created: 2026-08-12
updated: 2026-10-16
sources:
  - README.md
  - FUNCTIONALITY.md
summary: Documents how USB and Wi-Fi transports are used and how sessions are established and recovered.
---

# Transport and Session Model

## Transport paths

- **USB accessory / AOA (default)**: one USB bulk pipe multiplexing input and video (see below).
- **USB via `adb reverse` (opt-in legacy tunnel)**: localhost listeners reached through `adb reverse`.
- **Wi-Fi (optional)**: LAN listeners + DNS-SD discovery.
- **Host-discovered Wi-Fi (in development)**: independent `host/cmd/wifi-discover` browses `_drawsplay-tablet._tcp.local.` on one explicit IPv4 interface with link/TTL validation, fixed DNS/cache bounds and on-link endpoint filtering. The tablet advertisement direction was verified using an isolated Tab A8 NSD probe. Candidates are untrusted; this diagnostic is not wired to capture/input handlers or pairing. See `docs/wifi-discovery.md`.

## Session behavior

- Client auto-connects and auto-reconnects.
- Host maintains session ownership to avoid cross-transport conflicts.
- Video and input channels are coordinated but independently reconnectable.
- Host enforces single active tablet input owner in current mode.

## Runtime resilience

- ADB reverse maintenance (when the opt-in tunnel is enabled) uses fast retry for normal attach/unplug states and bounded backoff for command-failure states, with periodic adb-server self-restart on repeated hard failures.
- Android input/video channels auto-reconnect independently and preserve single-transport session ownership.
- Video pipeline restarts on congestion and capture faults; repeated capture-recovery exhaustion can trigger bounded display-level self-recovery on recoverable display backends.
- Windows lock-state transitions keep transport sessions alive while switching video output to black frames during host lock.
- VDD startup and plug failures attempt adapter-level self-recovery before mirror fallback. A display replaced by recovery or fallback is not treated as a user resolution change, so it never overwrites the tablet's stored preference.
- Decoder-bound detection: repeated queue-only step-downs that don't help while the stream sends under half its rung mark the tablet's decoder as the bottleneck; the host restores the rung and ignores queue-only congestion for that session (slow sends still step down).
- Windows adaptive bitrate is damped: two congestion reports within 2 s are needed, reports in the first 3 s after an encoder (re)start are ignored, the ladder never drops below a quarter of its top, and encoders use a 125 ms VBV so keyframes are not starved (previously caused blocky, low-detail video).
- Windows capture fps is capped at the tablet panel's refresh rate (from `hello`, else client stats, else 120). Capturing faster than the panel (e.g. 144 fps on a 120 Hz tablet) backed up the decoder and caused periodic hitches.
- Known issue: reinstalling the host while a tablet is connected can make the first VDD display detach a few seconds after creation; self-recovery normally brings it back on the first attempt.

## USB accessory (AOA) transport

Default ADB-free path (`aoaEnabled`, on by default): the host switches the tablet into Android Open Accessory mode and multiplexes input and video virtual connections over one USB bulk pipe (link frames `[type][id:2][len:2]`), feeding the same handlers as the TCP listeners with transport `usb`. Implemented in `host/aoa_*.go` and Android `transport/Accessory*.kt`. See `FUNCTIONALITY.md` section 3.0.1 and [AOA research](aoa-transport-implementation-research.md). Windows needs WinUSB, which the host installs as a self-signed driver package (no Zadig). Validated on Windows with a Samsung SM-X920 (Galaxy Tab S9 Ultra).

Related:

- [Protocol Reference](protocol-reference.md)
- [Operations and Runtime Controls](operations-and-runtime-controls.md)
