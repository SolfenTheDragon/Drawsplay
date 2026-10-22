---
title: Protocol Reference
type: topic
tags: [protocol, input, video, packets]
created: 2026-08-12
updated: 2026-10-22
sources:
  - FUNCTIONALITY.md
summary: High-level reference for current DrawSplay packet types and channel framing.
---

# Protocol Reference

Primary protocol details are authoritative in `FUNCTIONALITY.md` section 3.

The independent Wi-Fi major-2 codec now implements the proposed 36-byte
big-endian envelope with bounded control/config/AU lengths, header-first
payload borrowing and session epoch/sequence guards. Go and Kotlin
interoperability was verified over actual Tab A8 LAN TLS using synthetic
payloads. It is not wired to product sessions or a completed pairing flow;
USB framing is unchanged. See `FUNCTIONALITY.md` section 3.6 and
`docs/wifi-wire.md`.

Current packet families include:

- `hello` (client identity and capability metadata; Android tablets add `screenRefreshRates`, the panel's supported refresh rates at native size)
- `displayMode` (host → tablet; a user resolution/fps change the tablet stores as its per-host preference — never sent for VDD recovery or mirror fallback on Windows)
- `input` (touch/stylus events)
- `key` (keyboard passthrough events)
- `cursor` (host cursor updates to Android)
- video config/NAL framing on the video socket

Compatibility approach:

- Additive fields are preferred.
- Legacy keys remain accepted for backward compatibility in current implementation.

For exact examples and field-level semantics, use:

- [FUNCTIONALITY source summary](../sources/functionality-summary.md)



## Native media helper extensions

Additive video packets 2 (TILE), 3 (MODE), 4 (FRAME_META), hello `videoFeatures`, input `sq`, config `captureMode`, stats `echoMs`. Details: `FUNCTIONALITY.md` §3.5 and `native/drawsplay-media/PROTOCOL.md`. ffmpeg remains the fallback for every helper failure.
