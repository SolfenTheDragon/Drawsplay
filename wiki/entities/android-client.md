---
title: Android Client
type: entity
tags: [android, kotlin, client, tablet]
created: 2026-08-12
updated: 2026-08-12
sources:
  - README.md
  - FUNCTIONALITY.md
  - BUILD.md
summary: Android tablet app that auto-connects, decodes host video, captures touch/pen/keyboard input, and sends events back to host.
---

# Android Client

The Android app provides:

- Zero-touch connection/reconnect behavior.
- MediaCodec decode/render on Surface.
- Input overlay with touch/stylus capture and event batching.
- Keyboard passthrough events.
- Host policy handling for keep-alive and optional Wi-Fi trust material.
- Runtime Android compatibility profile (Android 8+ baseline) that automatically gates newer APIs (window metrics, frame-rate hints, low-latency decoder hints, modern package-info APIs) by SDK level.
- Decoder startup walks decoders × format tiers (`DecoderConfigPlan`) so a tablet whose decoder rejects an optional key (e.g. Unisoc rejecting `KEY_LOW_LATENCY`) still gets a hardware decoder; software is the last resort. Runs once at decoder start, never per frame.
- Client hello metadata now includes Android API level and capability flags so host-side behavior can adapt to client runtime features.

Key implementation root: `android/app/src/main/java/com/drawsplay/tablet/`.

Related:

- [Protocol Reference](../topics/protocol-reference.md)
- [Transport and Session Model](../topics/transport-and-session-model.md)
- [Security Model](../topics/security-model.md)
- Resolution recommendation: hello carries an optional `recommendedWidth/Height/Reason` computed from RAM tier and hardware decoder limits (`ResolutionRecommender`); the host web UI shows it as a hint.
