---
title: Security Model
type: topic
tags: [security, trust-boundary, auth, tls, csrf]
created: 2026-08-12
updated: 2026-10-24
sources:
  - README.md
  - FUNCTIONALITY.md
  - research/security-assessment.md
  - docs/windows-driver-signing.md
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
- The tablet's **Forget wireless host** waiting-screen action cancels the
  paired-session generation, closes pending TLS and established sockets,
  removes the paired input endpoint, and deletes encrypted trust plus atomic
  backup/pending files before allowing another pairing. Stale workers cannot
  republish a forgotten connection. Unreadable trust can be removed without
  accessing Keystore; deletion failure is explicit and leaves reconnect
  blocked. Identity keys remain intact. Forgetting is local, not a request
  to erase the peer record on an unreachable previous host.
- Forgetting now requires explicit confirmation of its tablet-local scope.
  Pairing cannot silently overwrite an existing saved host from the UI.
  Retry creates a fresh attempt and ignores cancelled generations. Approval
  guidance appears only after SRP proof and pinned TLS verification, not merely
  after code entry. Saved-trust and Keystore-identity failures are distinguished
  by typed errors, with no automatic key rotation, trust deletion or plaintext
  fallback; UI recovery copy does not expose raw exception contents.
- Windows WinUSB driver package is signed on the PC with a per-PC self-signed certificate (`CN=DrawSplay USB Driver Signing (<PC name>)`) trusted in LocalMachine `Root`/`TrustedPublisher`; the private key is created non-exportable and deleted right after signing, so the trusted certificate cannot sign anything else. Each re-sign replaces the certificate; uninstall removes it. The package only binds in-box WinUSB (no DrawSplay kernel code).
- Elevated helpers run as on-demand scheduled tasks that execute only the Program Files binary with fixed arguments.
- Elevated WinUSB/IDD signing and Parsec downloads use freshly created
  administrator-owned workspaces directly under machine Program Files,
  resolved through Known Folders rather than environment paths. An explicit
  protected DACL at creation grants inheritable access only to Administrators
  and SYSTEM. User ownership is rejected too, since an owner could rewrite
  the DACL from a filtered token. Signing rejects unexpected ownership/ACLs
  and reparse-point directories before creating certificate trust. WinUSB
  INF bytes must equal the generated binding before signing and installation;
  catalog generation verifies its contents match the INF. This closes the
  user-writable TEMP substitution window in the elevated USB binding helper.
- Virtual-display driver signing is separate from host Authenticode. Unsigned
  IddCx catalogs are signed by the elevated installer with a unique per-PC
  certificate, sharing the WinUSB signing helper but using a distinct certificate
  subject. The public certificate is trusted in LocalMachine Root/TrustedPublisher;
  its non-exportable private key is deleted before trust is added. Files must match
  the embedded package and are staged under Program Files, not user-writable
  settings/temp. Already trusted catalogs are preserved; invalid signed catalogs
  fail explicitly. Interactive setup discloses machine-wide trust; quiet installs
  authorize it and `-no-driver-install` opts out. Uninstall removes certificates
  after package removal; a package-removal failure retains trust with a warning.
  Secure Boot, test mode and signature enforcement are unchanged. The build scripts
  still do not obtain Microsoft signing or preserve a returned Microsoft-signed
  package through driver regeneration. See the
  [driver signing walkthrough](../../docs/windows-driver-signing.md) for manual
  submission, attestation testing versus HLK/WHCP certification, and the
  prerequisites for protected CI automation.

## Trust assumptions

Windows virtual-display setup tries the locally signed DrawSplay driver first.
Only after failure does it reuse Parsec VDD or, if missing and permitted,
download the pinned 0.41 installer into protected Program Files staging.
SHA-256 and a valid `Parsec Cloud, Inc.` Authenticode publisher are mandatory;
verification failure prevents execution. Working IDD installs never download
Parsec. Session startup only opens installed drivers, never installs them.
An existing broken Parsec package is not replaced. Separate Parsec ownership
tracking prevents DrawSplay IDD ownership from authorizing removal of a shared
Parsec package; ambiguous legacy ownership is treated conservatively.

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
