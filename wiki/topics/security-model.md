---
title: Security Model
type: topic
tags: [security, trust-boundary, auth, tls, csrf]
created: 2026-08-12
updated: 2026-10-22
sources:
  - README.md
  - FUNCTIONALITY.md
  - research/security-assessment.md
summary: Summarizes current security controls, trust assumptions, and residual risk areas.
---

# Security Model

## Current controls

- Local settings UI bound to loopback.
- CSRF token required for state-changing web API routes.
- Token redaction in settings/diagnostic API responses.
- Wi-Fi transport supports token + TLS policy/fingerprint model.
- Android's pinned Wi-Fi TLS factory finishes certificate verification before handing a connection to a channel, rejects malformed pins/non-positive timeouts, times out stalled handshake reads, and closes failed connections. Successful connections reset the handshake read timeout; channels own subsequent read policy. This is not the planned numeric-code pairing or mutual-authentication implementation.
- The accepted-socket TLS primitive keeps the host as TLS server even when it initiates TCP to the tablet. A real Tab A8/Linux LAN experiment verified both TLS roles, wrong-pin rejection and synthetic encrypted echo; see `docs/performance/tab-a8-wifi-tls-2026-10-22.md`. Advertisement data still cannot establish trust by itself.
- Android known-host secret storage uses encrypted preferences by default.
- Windows WinUSB driver package is signed on the PC with a per-PC self-signed certificate (`CN=DrawSplay USB Driver Signing (<PC name>)`) trusted in LocalMachine `Root`/`TrustedPublisher`; the private key is created non-exportable and deleted right after signing, so the trusted certificate cannot sign anything else. Each re-sign replaces the certificate; uninstall removes it. The package only binds in-box WinUSB (no DrawSplay kernel code).
- Elevated helpers run as on-demand scheduled tasks that execute only the Program Files binary with fixed arguments.

## Trust assumptions

The Wi-Fi-v2 components are independent of the legacy scaffold: client-first,
identity-bound SRP-6a code confirmation; paired SPKI mutual TLS; a new total
Android handshake deadline; Linux-private/Windows-DPAPI host trust; and
Keystore/AES-GCM Android trust without plaintext fallback. Go/Kotlin
interoperability and Tab A8 Keystore persistence/tamper rejection are tested.
The web UI Host overview pairing card (explicit Approve/Deny, CSRF-protected
`/api/wifi/pairing/*`, 404 without `DRAWSPLAY_FEATURE_WIFI`) and the opt-in Linux `wifi-pair` command and isolated tablet probe additionally
exercise host-discovered, host-initiated LAN enrollment, explicit local
approvals and both durable stores. Conscrypt's prehashed TLS signing requires
Keystore `DIGEST_NONE`; incompatible existing identities fail without rotation.
Linux host dialing pins the NIC and disables gateway routing. Other host OS
dialing is currently disabled, and full tablet route-change/VPN enforcement
is not established. Normal-app enrollment and capture/input sessions remain
unwired.
The isolated paired-channel preview now checks actual authenticated SPKI
values in bounded capabilities and binds a separately authenticated video
socket with a five-second single-use secret ticket. Control/video identities,
session ID and epoch must match; failed attachment invalidates the binding.
Fresh saved-trust channel association passed on the Tab A8 without
capture, decoding or input injection. This is not automatic reconnect or
product session authorization. Channel owners must close both sockets on
failure/revocation; clearing stored trust alone does not terminate sockets.
Their protocol has not received an independent cryptographic audit.
See `docs/wifi-pairing.md` for exact dependency pins, key derivation caveats,
caller obligations, revocation requirements and unverified platforms.

- Host machine is trusted by the local user.
- USB debugging is enabled only for trusted hosts.
- Optional network transports are configured with strong shared secrets.

## Residual risk to track

- The per-PC driver-signing certificate is a trusted root while DrawSplay is installed; its key no longer exists, but code relying on that root (for example the catalog) is trusted machine-wide.
- Local API authorization boundary is still process-local rather than user-authenticated.

Reference report:

- `research/security-assessment.md`
