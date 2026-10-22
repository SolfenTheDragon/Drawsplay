---
title: Security Model
type: topic
tags: [security, trust-boundary, auth, tls, csrf]
created: 2026-08-12
updated: 2026-08-12
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

- Host machine is trusted by the local user.
- USB debugging is enabled only for trusted hosts.
- Optional network transports are configured with strong shared secrets.

## Residual risk to track

- The per-PC driver-signing certificate is a trusted root while DrawSplay is installed; its key no longer exists, but code relying on that root (for example the catalog) is trusted machine-wide.
- Local API authorization boundary is still process-local rather than user-authenticated.

Reference report:

- `research/security-assessment.md`
