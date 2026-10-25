---
title: Host Service
type: entity
tags: [host, go, windows, linux]
created: 2026-08-12
updated: 2026-10-16
sources:
  - README.md
  - FUNCTIONALITY.md
  - BUILD.md
summary: Go desktop service that owns transports, capture/encode, input injection, web settings, and session lifecycle.
---

# Host Service

The host runs on Windows/Linux and orchestrates:

- Input and video listeners (USB accessory/AOA by default, legacy adb reverse opt-in, optional Wi-Fi).
- Virtual display lifecycle.
- Capture/encode pipeline selection and restart behavior.
- Input and keyboard injection.
- Local settings web UI and autostart controls.
- Self-healing runtime behavior for common failure modes (ADB reconnect turbulence, capture restarts, and Windows virtual-display recovery before fallback).
- Windows privacy-preserving lock behavior by streaming black frames while keeping active client sessions connected.

Key implementation root: `host/`.

Related:

- [Architecture Overview](../topics/architecture-overview.md)
- [Transport and Session Model](../topics/transport-and-session-model.md)
- [Security Model](../topics/security-model.md)
