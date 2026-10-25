---
title: Architecture Overview
type: topic
tags: [architecture, host, android, display]
created: 2026-08-12
updated: 2026-10-16
sources:
  - README.md
  - FUNCTIONALITY.md
summary: System-level architecture for host and Android client, including display, input, and channel responsibilities.
---

# Architecture Overview

DrawSplay is split into two primary runtime components:

1. **Host (Go)**: owns transport listeners, virtual display management, capture/encode, and input injection.
2. **Android Client (Kotlin)**: owns decode/render and touch/pen/keyboard event capture.

Core channels:

- Input channel (tablet -> host)
- Video channel (host -> tablet)
- Local web settings API (host localhost only)

Resilience and compatibility highlights:

- Windows host attempts in-process VDD recovery (adapter restart + display replug) before degrading to mirror mode.
- Windows lock-screen privacy behavior keeps sessions alive but switches video to black-frame streaming until unlock.
- Legacy USB (`adb reverse`, opt-in) maintenance favors fast reconnect probing on expected attach/unplug states.
- Android client runs one APK for Android 8+ and selects modern APIs only when the runtime SDK supports them.

Transport defaults:

- USB accessory mode (AOA over WinUSB/libusb) is the primary path; `adb reverse` is an opt-in legacy tunnel.
- Optional Wi-Fi transport is feature-flagged.

Related:

- [Transport and Session Model](transport-and-session-model.md)
- [Protocol Reference](protocol-reference.md)
