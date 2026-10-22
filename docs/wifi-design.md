# Wi-Fi transport design -- Phase 0 review draft

Date: 2026-10-22. Branch: `Wireless_Implementation`.
Source examined: `fe52f8592f4efac60d137dde297c0a1573142a80`.

**Status: owner authorized gated implementation on 2026-10-22.**
Implementation starts with the external USB measurement preparation tool,
not product transport changes. USB source and installed applications are
unchanged. No Wi-Fi mode was enabled, no pairing was attempted, and no host
networking settings were changed. Authorization is not a waiver of the
remaining software USB measurement gates; incomplete required software
evidence still blocks Phase 1. See the owner's scope revision below.

**Blocking evidence gap:** a repeatable full USB baseline does not exist in the
examined repository. The attached Tab A8 is usable for measurements, but the
installed host differs from the checkout and the first passive observation
contained a restart. Camera/physical-interaction measurements are no longer
blocking requirements. The
[baseline readiness report](performance/wifi-usb-baseline-readiness-2026-10-22.md)
records real CPU/memory observations and raw output, explicitly not a passing
baseline. Crypto overhead has NOT been measured.

## 1. Intent, scope and decision status

### Owner revision: software-only baseline, no camera or physical interaction

On 2026-10-22 the owner explicitly removed camera checks and required
operator-driven physical interaction from the non-negotiable baseline.
Do not block implementation on obtaining a camera/photodiode, optical
frame counting, physical taps/stylus actions or operator-driven lifecycle
tests. Those checks are optional future QA, not prerequisites.

This revision supersedes contrary mandatory language in sections 3, 12,
13 and 14. Software measurements and automated tests remain required:
CPU, memory, allocation/queue bounds, session stability, available decode/
presentation counters with their actual semantics, and automated timing/
reconnect/input logic checks. Software timing is a proxy, never measured
photon latency; synthetic input cannot prove physical pen fidelity.
Unmeasurable physical metrics are reported as NOT MEASURED / waived from
the baseline gate, not zero or PASS.

The owner has not waived USB protection, artifact provenance, repeatability
or observed software regressions. The invalid prior trial remains invalid.
Power instrumentation is not silently waived by this revision: use the
original permitted software battery proxy where readable, label charging/
vendor limitations and report uncertainty; do not require an external
meter when a valid permitted proxy is available.

### Owner clarification: LAN-only and discovery-first

The owner requires all DrawSplay Wi-Fi discovery, pairing, control, video
and input traffic to remain on the selected local network, with no internet
connection, cloud service, public DNS lookup or external rendezvous. Bind
sockets to that network/interface and validate destinations against its
on-link prefixes; do not assume every private address is local or every
globally scoped IPv6 address is remote. Reject off-link endpoint hints,
including QR/manual/discovery hints, rather than follow a default gateway
or VPN route. Revalidate on route/network changes and fail closed. Link-local
discovery uses the appropriate multicast scope/TTL. Application routing
controls cannot stop a malicious router from forwarding or copying LAN
packets; this is not a claim to control the surrounding infrastructure or
unrelated Android/host OS internet traffic.

The **host discovers tablets and initiates both TCP connections**. The tablet
advertises availability while Wi-Fi display mode is enabled; it does not
search for hosts. QR is an optional backup, not the primary pairing path.
Discovery is untrusted: first pairing still requires a locally verified
trust exchange, then future reconnects are automatic. Proposed default:
select a discovered tablet on the host, then enter the host's short pairing
code on the tablet using a vetted PAKE. This confirmation mechanism is an
author recommendation, not an owner-approved detail. Network names and IP
addresses never establish trust automatically.

Add optional LAN Wi-Fi first, then practical routerless links, without changing
the behavior or performance of the existing AOA USB product. The low-memory Tab
A8 and Android 8/API 26 are first-class constraints. Screen confidentiality and
authenticated input injection are release conditions, not optional settings.

This is a project-sized change, not a transport-class rename. The repository
already has an experimental Wi-Fi scaffold, but it lacks the required pairing,
lifecycle, buffering, diagnostics and hardware evidence.

The following are **author recommendations**, not decisions the owner has
already approved. In unattended discovery, unresolved choices remain explicit
approval items instead of prompting, guessing consent, or implementing.

### Approaches considered

| Approach | Advantages | Tradeoffs / decision |
| --- | --- | --- |
| Generalize USB and Wi-Fi through a new common transport/pump/decoder | Less duplication | Changes working USB framing, buffering, dispatch and ownership; insufficient baseline to justify. Reject for initial implementation. |
| Keep USB intact; add an isolated Wi-Fi session selected once at connection time | Strongest reviewable USB boundary; existing OS capture/injection primitives and pure policies reusable | Wi-Fi needs its own bounded receive/feed/pump ownership where current shared code allocates. Some focused duplication and a once-per-session integration seam. **Recommended.** |
| QUIC/datagrams or custom UDP video immediately | Can avoid TCP retransmission head-of-line delay | API-26 library/native packaging cost, loss recovery, MTU, crypto and congestion complexity; no device evidence. Defer; reconsider only if TCP fails measured gates. |

Recommended initial security: platform TLS with pinned P-256 host identity,
TLS 1.3 on API 29+, authenticated ECDHE/AEAD TLS 1.2 on API 26-28, plus
per-device credentials and authenticated channel association. Recommended
routerless modes: Linux NetworkManager AP, conditional Windows hotspot, and
tablet local-only hotspot as a common fallback. Wi-Fi Direct is optional
experimental work, not the only way routerless mode can function.

Non-goals: cloud/accounts/relay, internet dependency, multiple simultaneous
tablets, remote administration, silent router/firewall/VPN modification, new
capture backends, or a USB performance refactor.

## 2. Current architecture and evidence map

Repository-wide discovery covered Android implementation/tests/resources/build
configuration, host core and platform implementations/tests, web assets, native
C capture/encode, Windows IDD, packaging, scripts and CI. Binary assets,
generated outputs, Gradle's binary wrapper, compiled drivers and dependencies
were not reverse-engineered. Root behavior/build/security references and
relevant wiki/performance references were also examined. This is source
discovery, not a completed adversarial security audit or hardware certification.

### 2.1 Transport and framing

| Surface | Current behavior | Evidence |
| --- | --- | --- |
| Startup | AOA on; ADB reverse opt-in; Wi-Fi requires `DRAWSPLAY_FEATURE_WIFI` and `wifiEnabled` | `host/runtime_host.go`, `feature_flags.go`, `config.go` |
| AOA | OS USB discovery/control requests, one bulk byte pipe; no app-level cryptographic identity beyond physical/OS permission | `host/aoa_protocol.go`, `aoa_transport.go`, `aoa_linux.go`, `aoa_windows.go` |
| Outer AOA link | Big-endian `[type:u8,id:u16,len:u16]`, payload <=16000 bytes; tablet sends `DSAOA` + version 1, host answers; OPEN kind input/video creates virtual connections | `host/aoa_link.go:18-44`; Android `transport/AccessoryFraming.kt`, `AccessoryLink.kt` |
| App restart | Tablet instance nonce changes; host drops old virtual connections and re-handshakes; tablet HELLO keepalive every 3 s | AOA link implementations and tests |
| Legacy USB/ADB | Loopback input 27183/video 27184, optional per-serial `adb reverse`; retry/backoff, no ADB required by default | `host/adb.go`, `runtime_host.go` |
| Input/control | 13-byte mux-v1 header (`DS`, version, stream, flags, sequence, length) carrying JSON; legacy newline JSON still accepted | `host/usbmux.go`, `input.go`, `protocol.go`; Android `UsbMuxFrameCodec.kt` |
| Video downstream | `[type:u8,len:u32,payload]`; config, complete Annex-B NAL units; optional tiles/mode/frame metadata | `host/protocol.go`, `video.go:1287-1302`, `native_media.go` |
| Feedback upstream | Newline JSON on video connection; actual Android interval 250 ms, though some comments still say 2 s | `host/feedback.go`; Android `video/VideoChannel.kt:225-263` |
| Settings | Loopback-only HTTP 27180 with CSRF and local-origin checks; Wi-Fi must never expose this listener | `host/webui_server.go`, `webui_security.go` |

AOA is not ADB forwarding: its virtual `net.Conn`/Android `Socket` objects feed
the existing application handlers directly. The current setup is confirmed
AOA, even though the attached composite device also exposes a debugging
interface.

### 2.2 Handshake and session lifecycle

Input sends a JSON `hello` (identity, native display, refresh rates, codecs,
video features, optional trust token). Host resolves per-host display
preferences through `hostInfo` -> `modePrefs`, publishes `sessionPolicy`, and
accepts input only after hello. The video path waits up to 3 s for identity and
preferences before creating its display.

`host/session.go` coordinates input/video by **transport string**, not a
cryptographically authenticated device/session ID. It rejects a different
transport while one owns the session; it does not preempt Wi-Fi just because
USB arrives. Same-transport video reconnect is gated by reservation/handoff,
startup debounce and 3 s output silence; teardown can wait up to 10 s.

`streamVideo` creates and defers closing the display; capture faults retry
with bounded backoff, can recover/recreate a display, and can fall back to
mirroring. Disconnect cancels capture, closes the socket and releases
ownership only after teardown. A 30 s watchdog and idle-display cleanup are
backstops, not an acceptable Wi-Fi liveness deadline.

Android's `MainActivity` owns input/video. Each channel currently races
candidates independently (AOA, localhost, then discovered Wi-Fi with 200 ms
staggering). Endpoint order is a preference, **not guaranteed USB ownership**.
The activity starts/stops discovery; background keep-alive uses a tiny
headless `ImageReader` sink. There is currently no foreground service.

### 2.3 Capture, encoding and input

- Windows: own IddCx VDD on Windows 10 1903+, DXGI/FFmpeg `ddagrab`, GPU-owning
  encoder preferred; NVENC/AMF/QSV/software fallbacks, zero-copy probes where
  usable. Display rect maps injected touch/pen/keyboard.
- Linux X11: vkms/RandR extended output, otherwise X-screen mirror;
  `x11grab` and VA-API/software/vendor candidates. Optional native XShm,
  damage-driven VA-API helper, with FFmpeg fallback.
- GNOME Wayland: Mutter virtual monitor + PipeWire/GStreamer. KDE Plasma 6:
  KWin virtual output + PipeWire. sway/Hyprland: headless output +
  screencopy. Unsupported compositors may only mirror XWayland's X11
  content, not native Wayland windows, or fail if no capture path exists.
- Current low-latency profiles disable B-frames, use roughly 2 s GOPs and
  125 ms VBV; hardware/software details vary. PipeWire/screencopy generally
  cap at 60 fps; capable X11 VA-API may allow 120. Do not change these USB
  defaults as part of Wi-Fi.
- Host bitrate adaptation steps down approximately 0.6x with 1.5 s cooldown,
  probes after 30 s clean, and penalizes relapse for 5 minutes. It restarts
  the encoder, not the display; it does not adapt resolution.
- Input is event-driven on Android's I/O worker, preserves historical samples,
  and includes pressure/tilt/orientation/buttons. Queue: 32 batches, contact
  merge cap 512 samples, latest hover preferred. Current overflow can evict
  an oldest non-move when no move is available; do not claim all boundaries
  are unconditionally preserved from the prose documentation.
- Host injects through uinput/compositor mapping on Linux and synthetic
  pointer/SendInput on Windows. `cancelAll` releases contacts on owned input
  disconnect; lock/input-disable behavior remains authoritative.

Evidence: `host/{display,pipeline,ffmpeg,native_media,ratecontrol,congestion,
inject,input,video}*.go`, `drivers/windows/idd/`, `native/drawsplay-media/`,
Android `input/`, `video/`, and associated tests.

### 2.4 Existing Wi-Fi is a scaffold, not the requested product

`host/wifi.go` already creates TLS listeners (minimum TLS 1.2), a persistent
P-256 certificate and DNS-SD advertisement. Default ports are 37183/37184;
default bind is `0.0.0.0`. Settings validation requires TLS and a token when
Wi-Fi is enabled, although lower-level listener code still contains a
plaintext branch. Input hello and video auth compare the configured shared
token. There is no per-device revocation or authenticated cross-channel
session binding.

Android NSD uses `_drawsplay._tcp.`, TXT metadata and a stored leaf-certificate
fingerprint. Trust arrives via host `sessionPolicy`, normally over USB; this
is useful prior art for bootstrap, not a complete first-use LAN pairing flow.
There is no QR/code enrollment, UDP fallback, manual endpoint flow, roaming
network binding, or hotspot controller. Storage attempts encrypted preferences
but falls back to ordinary private preferences on failure.

Keep this scaffold disabled. Replace/harden Wi-Fi-only paths rather than
advertising that the current feature flag already satisfies this design.

### 2.5 Documentation discrepancies

`FUNCTIONALITY.md` remains the behavior reference, but some passages lag code:
8 concurrent input connections versus one per listener plus global ownership;
2 s versus 250 ms feedback; AOA allegedly excluding ADB in all cases versus
the attached composite device; and unconditional boundary retention versus
current queue overflow. Some wiki references still describe older transport
or cursor behavior. `BUILD.md` says SDK 34 while Gradle targets/compiles SDK 36.

This design records these discrepancies without refactoring or silently
changing the existing USB behavior. Update directly affected references with
approved implementation, and preserve compatibility tests.

## 3. USB invariant and measurement gate

### 3.1 What remains untouched

Do not change AOA identification, enumeration, OS backend, bulk packet/ZLP
rules, outer frames, mux-v1 framing, USB socket settings, USB retry clocks,
input serialization/coalescing, capture pipeline selection, decoder feed or
render/drop policy. Do not add a wrapper, encryption, copying, allocation,
new timer, per-frame branch or transport dispatch to USB.

USB-only startup must not load a Wi-Fi crypto provider, start a service,
acquire network/CPU/wifi/multicast locks, scan, advertise, parse Wi-Fi
settings repeatedly, or start Wi-Fi threads. If Wi-Fi is enabled but USB owns
the session, suspend active discovery/streaming and low-latency locks;
bounded established trust state can remain persisted, not a second decoder.
Explicit host enrollment/discovery work while USB is active must be measured
as a separate opt-in activity, not hidden background cost.

Natural seams already exist at accepted connections and Android connection
selection. Select **one complete input/video session once**, not one endpoint
per channel/frame. Preserve existing `net.Conn`/socket use for USB; no generic
transport framework is needed.

### 3.2 Necessary integration changes require separate approval

| Proposed existing surface | Why it might need a surgical edit | USB risk / gate |
| --- | --- | --- |
| Host runtime startup and feature-gated settings/UI | Discover tablets and dial independent Wi-Fi sessions; show enrollment/diagnostics | Prove no new USB-only workers/startup activity; no runtime start before baseline |
| Android activity entry/session selection | Launch Wi-Fi-specific session/service only when selected; USB wins | Keep current USB activity/input/video behavior; changes to selection can affect reconnect and need explicit approval |
| Host session ownership and disconnect registration | Bind authenticated device/session, perform serialized handoff | Must not alter normal USB acquisitions; race/property tests plus real reconnect baseline |
| Existing USB policy bootstrap | Optional transfer of Wi-Fi trust after user consent | **Not approved by default**; separate Phase 2 change, no unsolicited new USB messages |
| Shared video decoder/pump | Pooling would otherwise change ownership/release contracts | Do not modify initially; Wi-Fi-specific bounded adapters/feed use existing pure policies. Revisit shared refactor only with permission and measured evidence |

Approval of a future code diff is required if its USB risk exceeds the
reviewed connection-time seam. Stop rather than discovering a need to alter
USB in the middle of implementation and proceeding silently.

### 3.3 Baseline preparation, not a fabricated Phase 0 pass

**Current scope:** the owner revision in section 1 removes camera and
physical-interaction checks from this gate. The measurement table below
retains their definitions for optional future QA; only software-automatable
baseline checks are required for implementation progression. Historical
reports and the initial collector's always-BLOCKED acceptance output are
not retroactive verdicts under the revised gate. Implement a separately
labeled software-baseline assessment before using it to authorize changes.

The two instructions "build a baseline before changes" and "no implementation
code in Phase 0" leave an instrumentation sequencing decision. This draft
does not resolve it by writing a hidden benchmark app. Recommended: approve
an **instrumentation-only preparation gate**, with no product changes, before
Phase 1. Device interaction/restarts/installs require a scheduled measurement
window so the current working session is not used as a disposable test rig.

Preserve and hash current host/APK artifacts, establish exact build provenance,
and keep them available for rollback. Build tools/harnesses separately; do not
overwrite the working installation to make the baseline match checkout.

The future harness must measure:

| Metric | Measurement contract |
| --- | --- |
| Capture/source to visible tablet latency | Deterministic source pattern with frame IDs, simultaneous host/tablet high-speed camera or photodiode capture; record camera rate, scanout/rolling-shutter error and p50/p95/p99. API callbacks are software proxies only. |
| Useful sustained FPS and drops | Count unique source sequence IDs visible on tablet, separate source idle/duplicates, encode loss, network discard, decoder discard and presentation loss; fixed animation/video/text workloads. |
| Host CPU | Interval process CPU for host plus every capture/helper child generation; one-core and whole-machine-normalized values labeled; compositor/system CPU separately. |
| Tablet CPU / RSS / PSS | PID-generation-aware CPU deltas; PSS/RSS/swap/graphics/native heap at low cadence, codec process/system contribution separately. Quantify ADB/probe overhead. |
| Power proxy | Fixed brightness/radio/charger/temperature; calibrated external power meter preferred. Otherwise discharging charge-counter slope and voltage over long windows, labeled net-device proxy, no app-specific watts claim. |
| Input application RTT | Known tablet input -> dedicated host responder observes OS-injected event -> software acknowledgment; bounded test-only sampling, not per-event production ACKs. Report p50/p95/p99 and lost boundaries. |
| Input-to-photon | Controlled host drawing/flash target and optical observation of real tap/stylus; synthetic events exercise logic only. No active-pen claims from a capacitive-only device. |
| Startup / reconnect / resource use | Cold/warm connection times, thread/socket/codec/lock counts, alloc bytes/op and counts, GC pauses, background battery and 8 h trend. |

No screenshot/screen recording of private desktop content is needed for
Phase 0. Later optical experiments use a synthetic desktop only, with owner
consent and local evidence storage.

Use fixed scene/resolution/FPS/bitrate/brightness and synchronized run IDs;
warm for 5 minutes, collect at least 5 minutes per workload, and start with at
least 10 matched A/B pairs across independent session runs. Randomize order,
cool between runs, keep battery/thermal state matched, log other host load and
reject/record disturbances. Treat independent runs, not correlated frames,
as statistical units; use block bootstrap intervals for latency tails.
Increase repetitions until sensitivity is adequate.

Compare pre-change USB against candidate USB with Wi-Fi off, then candidate
USB with Wi-Fi enabled/idle and USB owning the session. Test AOA separately
from legacy ADB; do not silently substitute ADB as "USB". Repeat the **same
full baseline after every phase**, and after any proposed shared-code change.

Any statistically meaningful adverse shift blocks progression. Correct
multiple comparisons (for example Holm at familywise 5%). Failure to detect
significance is not equivalence: report uncertainty and instrument resolution.
Equivalence margins/sensitivity must be approved before trials; no 5% or
one-frame regression allowance is assumed. Exact zero change in stochastic
CPU/power cannot be mathematically established by a finite experiment.
Missing metrics or inadequate power mean **inconclusive/block**, not PASS.

## 4. Modes and honest platform feasibility

Network topology is independent of X11 versus Wayland; only the existing
capture/input permissions differ. Being on the same SSID does not guarantee
peer reachability: VLANs, guest isolation, VPN policies and firewalls matter.
The host may be wired to the LAN; it need not have its own Wi-Fi NIC.

| Priority / mode | Linux X11 and Wayland | Windows 10/11 | Android 8+ and degraded working path |
| --- | --- | --- | --- |
| 1: existing LAN/AP | Required, ordinary TCP/TLS on selected reachable NIC | Required; private-network firewall consent | Join network normally; NSD then fallbacks. Ethernet host works. |
| 2: host hotspot | NetworkManager AP if driver/firmware supports AP; AP+STA separately capability-tested; may disconnect upstream Wi-Fi | Mobile Hotspot where OS/profile/driver permits; WinRT tethering capabilities/packaging vary; offline hotspot not universally reliable | Join as an ordinary station. If auto-join unavailable/denied, show Settings instructions and QR credentials. |
| 3: tablet hotspot | Host joins ordinary WPA2 AP; no special display-server dependency | Host joins ordinary WPA2 AP | `startLocalOnlyHotspot` exists on API 26, subject to permission/OEM/tethering conflicts. Hold reservation only for session. Prefer this local-only API over silent system tethering. |
| 4: Wi-Fi Direct | Android P2P with `wpa_supplicant`/driver P2P support; NetworkManager coexistence and privileges not uniform | Native/WinRT Wi-Fi Direct support exists, but desktop APIs/pairing and Android interoperability vary | Supported API does not guarantee hardware/OEM support. Experimental only after real interop tests; fallback to either AP or LAN/USB. |
| Not a baseline mode: Wi-Fi Aware | No broadly deployable common Linux desktop peer stack | No broadly deployable common Windows desktop peer stack | API 26+, optional hardware, runtime availability and concurrency limits. Not assumed on Tab A8; omit from required path. |

### 4.1 Selected routerless recommendation

On Linux with NetworkManager, query AP support and `iw phy` valid interface
combinations; check regulatory/band constraints and AP+STA channel
concurrency. Use NetworkManager D-Bus with a dedicated temporary connection,
not arbitrary shell execution. Require explicit consent before interrupting
the current station connection. Configure WPA2-PSK/CCMP minimum and verify
the result: `nmcli hotspot` can degrade to WEP on unsupported hardware, which
is **not acceptable**. Do not promise 5 GHz on every regulatory/adapter setup.
Never change global rfkill, power-saving or system network configuration
silently; restore only the profile this app created.

On Windows, prefer a guided Mobile Hotspot setup, with optional automation
only after capability/API contract checks and user consent. The
`NetworkOperatorTetheringManager` API is profile/capability dependent; opening
`ms-settings:network-mobilehotspot` is a valid degraded path. Offline systems
may refuse Mobile Hotspot. A Wi-Fi Direct autonomous/legacy group-owner
publisher is a possible separately tested enhancement, not a guaranteed
replacement. Deprecated `netsh wlan hostednetwork` and a "Hosted network
supported" result are not a reliable modern capability test.

When host AP setup is unavailable, start a tablet local-only hotspot; show
its SSID/password locally and guide the host to join. API 26 returns
framework-chosen credentials; configurable modern SoftAP APIs must be
version-gated, not called reflectively. Handle `ERROR_NO_CHANNEL`,
incompatible mode, tethering prohibition and location/permission denial.
Do not assume every Wi-Fi-only Tab A8 firmware exposes the same Settings
tethering UI. If local-only hotspot is denied, offer an existing LAN or a
portable AP; if neither endpoint can create/join a reachable wireless link,
the honest fallback is USB, not a claim that software can supply absent
radio functionality.

Android 10+ connection specifiers/suggestions involve system consent and
different persistence semantics; they do not permit silent arbitrary
network selection. On API 26-28, prefer user-driven Settings joining over
rewriting saved network profiles. An invitation can carry the app-created
AP credentials with consent; it must not extract unrelated saved passwords.

### 4.2 No internet, captive portals and routing

Select a Wi-Fi `Network` with `ConnectivityManager`, `TRANSPORT_WIFI` and
local reachability, without requiring `NET_CAPABILITY_VALIDATED` or
internet service. On local-only requests explicitly avoid an internet
requirement. Retain that `Network` for the session.

Bind tablet TCP listeners to the selected Wi-Fi Network's local addresses;
verify accepted peers and route/interface association before enrollment or
TLS. Wrap accepted sockets in TLS as described in section 7.1. Use
`Network.bindSocket` for Wi-Fi UDP sockets and network-specific resolution
if needed; do not resolve discovered local addresses via public DNS.
Use `network.socketFactory` only for any explicitly required outbound
Wi-Fi socket, not for the normal host-initiated connection.
Do **not** process-bind the app: process binding can redirect loopback/USB
fallback and unrelated traffic. Use network-scoped NSD registration where
available behind API gates; older registration may use system routing, so
selected-network UDP/manual tablet address remains the working fallback.
A bundled mDNS implementation must demonstrably bind its own sockets
correctly before it is relied on.

Show "This Wi-Fi has no internet; keep this connection for DrawSplay" if the
system asks. Per-socket routing prevents accidental mobile/default-network
use, but cannot force an OEM to keep a network associated, disable captive
portal checks, defeat an always-on VPN lockdown, or override a user's choice
to disconnect. Never fake internet connectivity or disable OS protections.

## 5. Discovery, first pairing and durable trust

### 5.1 Discovery pipeline

1. Tablet starts bounded input/control and video listeners on its selected
   Wi-Fi address, then registers `_drawsplay-tablet._tcp.` using Android
   NSD. A distinct service type avoids confusing older host advertisements
   with tablet endpoints. Advertise protocol range,
   input/video ports, opaque tablet identity and pairing availability. No
   token, Wi-Fi password, private identifier, or trusted fingerprint source
   in TXT. A fingerprint in TXT is merely an untrusted hint.
2. Host browses/resolves that service only on the selected LAN NIC and
   displays available tablets. It may retry previously paired tablet
   addresses on that same on-link network in parallel; authenticate every
   attempt. Remembered IP/name and advertised identity are not credentials.
3. Tablet registration has one lifecycle generation, bounded retry on NSD
   errors, stale callback rejection and explicit unregister on Stop,
   network change or USB ownership. Host candidates/resolutions are bounded
   and expire. No tablet-side host browsing is started.
4. After about 3 s without a usable candidate, parallel selected-interface
   host-originated UDP discovery on proposed tablet port 37182. Tablets
   answer while available. Limit packets to 512 bytes,
   bounded rate/candidates, random request nonce, protocol version, reply
   matching and unicast replies no larger than the request. No amplification,
   subnet sweep, credentials, or automatic trust. IPv4 directed broadcast
   per NIC, never blindly broadcast across VPN/virtual interfaces.
5. Host offers manual tablet IP/port if automatic discovery is blocked.
   QR is optional trust bootstrap, not a prerequisite for discovery.
   IPv6-only uses mDNS/link-local multicast where permitted or manual tablet
   address, not IPv4 broadcast. No subnet scans or cloud lookup.

Evaluate bundled pure-Java JmDNS for API-26 NSD reliability with reproducible
OEM tests, license/dependency maintenance, selected-network binding, thread
and multicast cost. Do not add it unconditionally to USB startup. If approved,
activate for tablet advertisement on NSD registration failure, not alongside
unlimited duplicate listeners. Stop discovery traffic on session acquisition
and release its multicast lock. It is not a
workaround for AP/client isolation or multicast actually blocked by the AP.

No candidates in 3 s is not proof of isolation. If authenticated manual/QR
unicast connects but multicast fails, say "Automatic discovery is blocked;
your connection still works." If both discovery and unicast fail, show
"Cannot reach this tablet. Check its availability and whether this is a guest
network that blocks devices from each other." Mark isolation/firewall as
**suspected**, not diagnosed from a timeout. No broadcast can cross an AP's
peer-isolation policy; use a non-guest LAN or one of the direct modes.

### 5.2 Primary pairing: discovered tablet plus authenticated code

Host shows discovered tablets. Selecting one initiates a bounded enrollment
connection without granting screen or input access. Proposed default: host
displays a short code and the user enters it in the tablet app; the tablet
asks for local approval before accepting a new host. Section 5.3's vetted
PAKE binds both identity keys, roles, version and handshake transcript.
Persist mutual trust only after key confirmation succeeds. Discovery
without a trust check is not a secure pairing option.

This makes PAKE selection/interoperability review an initial LAN release
gate, not optional work deferred by quietly making QR the default. Once
paired, host rediscovery and reconnect need no code or scan.

### 5.2.1 Backup: QR invitation

The host's local-only settings page opens a 120 s enrollment window after
explicit user action and can display a backup QR invitation:

- Format/version and endpoint hints, selected-interface information.
- Host P-256 identity SPKI SHA-256 pin and opaque host ID.
- Random 256-bit single-use enrollment secret and invitation identifier.
- Expiry checked by host monotonic lifetime, not solely tablet wall clock.

Scan in the tablet with on-demand camera permission. Without a camera,
manually enter the invitation payload/long random code and host address;
typing an IP alone must **never** bypass identity verification or enrollment.
No cloud URL, browser redirect or external QR service is involved.

The tablet pins the scanned host key **before** sending its enrollment
secret over TLS. It generates its own Keystore P-256 identity and proves
possession. The host validates the secret/window, atomically consumes it,
shows the new tablet identity for local confirmation, and persists the
device credential only after approval. Until then: no capture, display
creation, input device ownership or injection.

Use per-device host-signed client certificates for subsequent mTLS.
Enrollment connections use the pinned server with application enrollment
authentication only; regular input/video require valid paired client
certificates. A pending enrollment cannot run protocol commands. Enrollment
and regular channel state are explicitly separate even if served on the
same selected input/control port.

TCP initiation remains host-to-tablet in both pairing modes. After a tablet
scans the backup host invitation, it awaits the host's inbound connection;
it does not dial the QR endpoint hints. Tablet permission/advertisement and
host discovery/manual tablet address still determine reachability.

An untrusted LAN cannot replace the out-of-band QR pin. Someone who can see
or photograph the invitation can race enrollment; expiry, single use,
host confirmation and physical-screen secrecy mitigate this, not magic
"zero-config" assurances. A compromised host/tablet is outside this link's
protection boundary. After pairing no human approval is needed on reconnect.

### 5.3 Short numeric pairing code

A six/eight-digit number used as a bearer token or encryption key is
unacceptable. A numeric path requires a reviewed PAKE (for example a
SPAKE2-family protocol with vetted interoperable implementations), binding
both identities, protocol version and transcript, then explicit host
confirmation. No home-grown PAKE or unauthenticated ECDH.

This is a **dependency/approval gate**, not an assertion that the current Go
or Android libraries provide PAKE. Select/review an API-26-compatible
interoperable library before planning that task; fail
closed if unavailable. Suggested controls: five attempts per invitation,
120 s expiry, bounded per-source and global rate limits, lockout requiring
local reopen, no distinguishable "valid digit prefix" responses.

### 5.4 Storage, reconnect, rotation and revocation

Store the tablet identity as a non-exportable Android Keystore key; use a
versioned Keystore-encrypted trust database in app-private storage. Hardware
backing is capability-dependent; StrongBox is not an API-26 assumption.
Report key/storage failures and require repair/re-pairing; never fall back
to plaintext secrets. Exclude trust data from cloud/device backup unless a
reviewed key-safe migration exists.

Host private keys and paired-device records use restrictive per-user file
permissions; Windows ACLs/DPAPI where applicable. Persist atomically. Keep
diagnostic host/device labels separate from authentication identity. A plain
`hello.device` string and current `host_id` are not credentials.

Explicit "Forget/revoke tablet" closes both active channels, lifts contacts,
invalidates session/attachment tickets, removes its trust and forces fresh
enrollment. Validate the revocation generation on resumed connections; rotate
TLS ticket keys/disable resumption if necessary so tickets do not bypass
revocation. Host-key replacement requires authenticated rotation over the
existing trust or explicit re-pairing; never silent TOFU. Expiry/clock errors
must not trigger an insecure fallback.

### 5.5 Optional USB-bootstrapped pairing

The existing `sessionPolicy` token/pin is evidence that USB bootstrap is
feasible, not permission to expand it silently. If separately approved in
Phase 2, send a bounded additive enrollment message after normal USB
handshake, with explicit "Trust this computer for Wi-Fi" consent. Exchange
public identities and a short-lived single-use credential over the
physically approved AOA channel. Persist no Wi-Fi trust before consent.

Only app-created hotspot credentials may be transferred, with separate
consent. Normal USB setup/video/input must not wait for this exchange;
old versions ignore unsupported capability messages. Do not change AOA
frames, add per-event acknowledgments or require ADB. USB interception by a
malicious physical accessory is outside the usual trusted-cable assumption;
show key confirmation if stronger physical-threat protection is required.

## 6. Security and cryptographic feasibility

| Option | API-26 feasibility / cost | Decision |
| --- | --- | --- |
| Platform TLS 1.3 + pinned identity | Platform default from API 29; current Go supports it. API 26-28 cannot be assumed to offer it | Preferred where available; use secure TLS 1.2 fallback below, not plaintext |
| Bundled Conscrypt/provider | Could supply newer TLS independently, but adds native ABIs, update responsibility, APK/startup/memory cost and testing | Only if a strict TLS-1.3-on-26 requirement is approved; not globally installed |
| Noise | Potentially compact with AEAD, but audited matching Go/Android implementation, identity/handshake/rekey/replay and maintenance still required | Not justified over existing TLS scaffold for first release |
| Custom AEAD framing | Primitive availability does not solve nonce, key exchange, replay, rekey or DoS | Reject custom cryptography |

For TLS 1.2 allow forward-secret ECDHE with ECDSA/P-256 and AES-GCM
(ChaCha20-Poly1305 only if the provider supports it); disallow TLS <1.2,
RSA key exchange, CBC and plaintext downgrade. TLS 1.3 suites/provider order
cannot be arbitrarily controlled with Android's TLS-1.2 cipher APIs. Pin the
SPKI and verify key possession, algorithm and expected identity/usage; do not
install a process-global trust-all manager or rely on DNS/CA names to
authenticate a self-signed local host.

The attached T618 advertises AES and PMULL instructions, making accelerated
AES-GCM a reasonable **hypothesis**, not a measured winner. Test actual
Android provider behavior against ChaCha on this tablet; software fallback,
thermal throttling and small-record overhead can change the result.

### Required crypto experiment

After approval, a separate instrumentation package (not replacement of the
working app) exercises the exact proposed provider/socket/engine settings:
TLS 1.2 and 1.3 where available, AES-GCM versus ChaCha where selectable,
1/4/16/64 KiB application writes, sustained 1/2/4/8/12 Mbit/s with bursty
IDRs, concurrent small input/control traffic, full/resumed handshakes, 60 s
warm-up then five independent 180 s trials per shortlisted configuration.
Pair plaintext loopback and encrypted loopback only in the isolated benchmark;
production never exposes plaintext. Repeat real-radio runs to distinguish
crypto CPU from radio/serialization effects.

Report p50/p95/p99 input RTT and write blocking, CPU/core-seconds, allocations,
GC, PSS/native/codec memory, thermal trajectory and calibrated power proxy.
Pre-register criteria before measuring: sustainable 2x selected video rate,
no input deadline failures, bounded memory, no additional app-owned per-frame
allocation/copy after warm-up. CPU/power budgets require owner-approved
measured limits; no Tab A8 performance number is invented here.

**Crypto result now: NOT MEASURED.** No on-device OpenSSL executable was
available. `simpleperf` exists, but its availability is neither a usable
crypto workload nor proof of profiling permission. No probe was installed.

TLS provides record authenticity/replay protection; disable 0-RTT for all
commands and input. Bind application sequence numbers to fresh session
nonces/epochs, device identity and channel role. Reject duplicate/stale
session commands and attach tokens. Never replay input after reconnect.
Pairing keys never appear in multicast, logs, metrics, QR screenshots saved
by the app, or repository evidence.

Port scanners will see an open selected listener; cannot promise invisibility.
Unknown peers get bounded TLS/enrollment work, no screen/input/host labels,
generic failures, deadlines and rate-limited local logs. Proposed limits:
8 concurrent handshakes globally, 2 per source, 5 s TLS/auth deadline,
4 KiB enrollment/control messages, finite candidate/rate-limit maps with
expiry and eviction. One authenticated active tablet. Validate credentials
before reserving a display/session or creating a Wi-Fi injector.

## 7. Session protocol and latency policy

### 7.1 Initial choice: separate TCP/TLS channels

Use separate authenticated TCP/TLS input/control and video channels,
`TCP_NODELAY` set on the underlying sockets before TLS. Input never queues
behind a video write or shares its writer mutex/queue. Control uses the input
connection but high-rate input never waits for a control request/response.
TLS setup/session binding happens once per connection, not per frame.

**TCP initiator and TLS role are deliberately independent.** Host dials
both tablet ports but acts as TLS server on those connected sockets;
tablet accepts and acts as TLS client. Go `tls.Server` can wrap a dialed
connection; Android's accepted TLS socket must explicitly use client mode
before handshake. This preserves the host server identity/tablet client
certificate model without requiring tablet discovery of hosts. Add a
Go/API-26 interoperability test before committing to this arrangement.
If the platform cannot support it reliably, return with a revised TLS-role
design rather than silently reversing the required TCP direction.

For initial numeric enrollment only, the dedicated bootstrap connection
has no pre-existing host pin: any provisional certificate handling is
strictly confined to that unauthenticated state. PAKE must authenticate
both presented identity keys and its channel/transcript binding before
trust is persisted or normal commands are enabled. Close it and establish
fresh pinned/mTLS connections after success. Never install global
trust-all behavior or treat successful bootstrap TLS as authentication.

TCP retransmission can stall video even with small queues; TCP_NODELAY
does not solve head-of-line blocking. TCP is selected for API-26 availability,
existing deployment, ordered bitstreams and simpler secure recovery.
QUIC offers independent streams and possibly DATAGRAM support only in a
chosen implementation; a reliable QUIC video stream still has its own HOL.
UDP requires authenticated fragmentation/reassembly, pacing, loss/reorder
handling, MTU discovery and congestion control. Revisit hybrid reliable
input/control plus deadline-limited authenticated video only if measured TCP
results cannot meet the weak-link profiles.

Reasoning, **not a measured latency claim**: 60 fps already has a 16.7 ms
frame period; 30 fps has 33.3 ms. Serializing 64 KiB at 8 Mbit/s takes about
65.5 ms, and 128 KiB at 1 Mbit/s takes about 1.05 s. Large kernel/TLS/app
buffers cannot be called low-latency just because memory is bounded.

### 7.2 Authentication, channel association and versioning

Define a Wi-Fi-only application protocol major 2; do not bump/replace AOA or
mux-v1 for USB. Authenticated control exchanges typed hello records with
min/max protocol version, device identity reference, fresh nonces, codec
profile/level, maximum dimensions/FPS/access-unit size, memory limits,
stats version, low-latency support, and feature bits.

After mTLS control succeeds, host creates a random 128-bit session ID and
separate random nonzero 64-bit epoch. The ID identifies the authenticated
control session; the epoch is its compact wire-generation tag, not an
authentication credential. Generate a new ID/epoch on control reconnect,
reject a collision with retained live/recent generations, and bind both to
the paired identity. Host also creates a 32-byte one-use video attach ticket,
expires it in 5 s, and binds it to that identity, ID, epoch and role.
Video mTLS must present the
same paired identity and consume the ticket before any config/video or
display allocation. Its typed attach record carries the ID and ticket;
the connection is associated with the epoch only after validation. A second
device cannot attach its video to another device's authenticated input.
Video-only reconnect obtains a fresh ticket on the existing control channel,
retains that session's epoch and increasing video sequence, and starts with
config/IDR; it does not reset input or replay queued events.

Both TCP connections use the fixed 36-byte envelope below; TCP read/write
boundaries and newline delimiters are not framing. Input/control payloads
are typed UTF-8 JSON with `type` and a 4 KiB maximum, including capability
lists and initial hello/attach records. Input retains existing compact JSON
field semantics, serialized into reusable buffers rather than current
per-event objects/byte arrays. Set duration/reserved fields to zero for
input/control; the timestamp is the sender's monotonic event/send time.

Proposed fixed big-endian 36-byte Wi-Fi envelope:
`type:u8, flags:u8, headerLen:u16, payloadLen:u32, epoch:u64,
sequence:u32, captureMonoUs:u64, durationUs:u32, reserved:u32`.
The bootstrap layout is fixed before negotiation; major 2 retains it,
headerLen is 36, and reserved fields/undefined flags are zero. Separate
types distinguish control, input, attach, config and video AU records.
Only bounded bootstrap hello/negotiation records use epoch/sequence zero.
The host's `sessionReady` establishes the nonzero epoch; every subsequent
session record must match it. Video attach already carries the assigned
epoch and is still untrusted until its identity/ticket checks pass.
Sequences start at one and strictly increase independently per direction
and channel; reject duplicate/decreasing values. Video-only reconnect
continues its prior sequence. Re-establish the full session before any
sequence wraps; never reset a counter inside an epoch. Session association,
TLS and these checks jointly prevent stale records crossing reconnects.
Payload limits are checked **before** checkout of a pool slot or reading.
An encoded access unit, not one arbitrary NAL, is the Wi-Fi scheduling/drop
unit. Config/SPS/PPS are retained in bounded session storage. No raw USB
frames are tunneled/encrypted/reformatted to create Wi-Fi.

No common version/codec/profile/size means an authenticated, bounded
`unsupportedVersion`/`unsupportedCapability` response and a clear upgrade
message. Never silently fall back from secure Wi-Fi to an old token/plaintext
protocol. Old USB clients keep their existing behavior. Update
`FUNCTIONALITY.md` and compatibility tests when this protocol is approved.

### 7.3 Bounded memory and ownership

Current code is **not allocation-free**: AOA frame parsing/writes allocate;
host FFmpeg pump allocates and copies read chunks, Annex-B splitting builds
lists; Android video allocates one byte array per packet and holds up to
128 pending NALs of potentially large size. These are existing USB facts,
not an excuse to change them here. Reusing those entire pumps for Wi-Fi
would not satisfy the requested new-path guarantees.

Recommended Wi-Fi-only ownership design:

- Reuse capture specs, display lifecycle primitives, injector primitives,
  decoder configuration tiers and pure backlog/scaling policies. Do not
  route USB through a new pool or callback.
- Independent host Wi-Fi AU parser/pump with startup-allocated slabs and
  byte limits; no per-read slices/lists/growing buffers or extra image
  copies. Frame ownership moves from parser to writer, then back to pool.
  Capture/encoder APIs can still require unavoidable format conversions.
- Android Wi-Fi receive/feed uses fixed leased AU slots. The lease is
  released only after bytes have been copied into MediaCodec-owned input
  buffers or discarded, not as soon as the network reader returns.
  No returning a buffer while the decoder still references it.
- Keep the existing USB `VideoChannel`/`VideoDecoder` unchanged. If the
  current decoder cannot accept leases without hot-path callbacks/branches
  affecting USB, implement a Wi-Fi-specific feed adapter/decoder session
  using the pure configuration policies. Focused duplication is preferred
  to unapproved USB refactoring; review its maintenance cost explicitly.
- Reject/renegotiate oversize AUs rather than allocate on demand.
  Disable tile mode initially on Wi-Fi: the existing up-to-32-MiB inflate
  and bitmap path is not automatically an acceptable low-memory fallback.

Proposed initial app-owned compressed-buffer ceiling: host 4 MiB, tablet
4 MiB (including AU slots, reassembly, config and control reserves).
Two queued AUs plus one in-flight slot; default max AU 768 KiB within that
total, downward negotiated by codec input capacity; no allocating a larger
slot on the fly. Parser overflow requests lower quality/fresh IDR or fails
cleanly. Decoder-owned memory, surfaces, GPU/driver, TLS native state,
kernel socket memory and stacks are **additional** and must be measured.
This is not a claimed total app PSS.

Start with one Wi-Fi decoder, normal Surface decode (no decoded-frame bitmap
copies), and a provisional total app PSS target below 150 MiB at 1280x800.
Treat this as a budget to validate, not proof that Android will never kill
the app. LMK can kill any app under system pressure; even an FGS cannot make
an absolute "never an LMK candidate" guarantee. On memory pressure release
queues/config/codec as appropriate, lower Wi-Fi quality, and provide clean
reconnect rather than OOM or endless allocation.

No new app-owned steady-state per-frame allocations or avoidable copies are
permitted. TLS encryption/decryption and copying compressed bytes into
MediaCodec buffers are unavoidable operations, not opportunities for an
extra intermediate array. Platform TLS/MediaCodec providers may allocate
internally; Java socket APIs do not guarantee otherwise. Profile them.
If the hard rule includes **all** framework/native provider allocations,
zero cannot currently be promised: select a proven bounded engine/provider
or keep the gate blocked. Do not silently weaken the rule to "low average GC".

### 7.4 Queue age, late dropping, keyframes and decoder resync

Bound by **bytes and age**, not only count. Initial Wi-Fi sender target:
one AU in flight, at most two waiting, queued age <= one frame period.
Discard superseded unsent complete AUs only when reference dependencies
allow; otherwise discard the dependency chain and resume with fresh
SPS/PPS/IDR. Do not drop random bytes/NAL fragments/reference P pictures
and hope the decoder catches up.

Kernel buffers are selected once per quality/session using measured rate
and RTT; begin testing 16-64 KiB video buffers rather than inheriting the
current 1 MiB settings. Query effective OS sizes (Linux may double requests)
and include TLS/encoder buffers. A minimum kernel buffer can still represent
too much delay on a 1 Mbit/s link. App queues do not bound data already sent
to TCP or held inside an encoder.

Wi-Fi-only write/feedback watchdog detects >150 ms no useful progress,
reduces quality, and can close/reopen video under the authenticated control
session to discard TCP backlog. Avoid global USB restart hooks. Rate-limit
IDR/restarts to prevent storms. If an encoder supports runtime force-IDR,
use it; FFmpeg fallback needs an encoder restart without display replug.
The 2 s baseline GOP is not itself a fast recovery guarantee.

Tablet's adaptive jitter slack starts at 0 frames on a clean LAN and can
grow to 1-2 frames, capped at 40 ms, based on inter-arrival variation and
late output; do not delay input or create a second unbounded queue. No
additional USB jitter buffer. Drop late presentations using valid
timestamps/dependency-aware recovery, not arbitrary latest compressed NALs.

On format change or >500 ms no decoder output despite valid AU receipt,
stop/flush/reconfigure the Wi-Fi codec using the existing compatible
configuration ladder, discard old epoch buffers and request fresh config/IDR.
Recover to software only with explicit degraded-mode warning and lower
quality; repeated failure ends the session with actionable diagnostics.

### 7.5 Input priority and fidelity

Separate small input socket, its own bounded writer and reserved buffers;
prioritize session cancel/up/button transitions over coalescible motion.
Preserve per-pointer contact order, pressure, tilt, orientation, buttons and
historical sample timestamps. Coalesce only contiguous compatible movement;
retain pressure/tilt extrema and time ordering within a fixed sample budget.
Never move a sample across down/up/cancel or a pointer/button change.

Overflow policy must be testable: drop stale hover first, then redundant
movement; if transitions alone exhaust the reserve, send cancel-all and
close/recover rather than lose an up event and leave a stuck pen.
No unlimited "preserve every sample" promise on an unusable radio.

Timestamp at capture with monotonic time; carry session/sequence to reject
replays. Estimate clock offset/uncertainty using periodic ping samples; raw
host/tablet clock subtraction is not one-way latency. One small control
ping per second supplies application RTT; no per-event handshake. TCP
retransmission metrics are not UDP packet-loss measurements; label them
accordingly. Wireless WMM/DSCP requests, if supported, are hints, not
guaranteed priority over other clients.

## 8. Adaptive quality

Adapt **Wi-Fi only** using 250 ms/1 s windows of app RTT, video byte delivery,
write blocking, oldest queue age, complete-AU sequence continuity,
reconnects, decoder backlog/output rate and thermal/memory signals.
TCP hides recovered packet loss; expose loss as unknown when unavailable
rather than deriving it from `frameDrops`.

Proposed initial H.264/no-B-frame ladder, constrained by negotiated decoder
limits, preserving aspect ratio and even dimensions:

| Rung | Encoded maximum | FPS | Target / peak bitrate |
| --- | --- | --- | --- |
| Q4 | 1920x1200 | 60 | 10 / 12 Mbit/s |
| Q3, initial Tab A8 | 1280x800 | 60 | 6 / 8 Mbit/s |
| Q2 | 1280x800 | 30 | 3 / 4 Mbit/s |
| Q1, weak 2.4 GHz | 960x600 | 30 | 1.5 / 2 Mbit/s |
| Q0, safe floor | 640x400 | 20 | 0.75 / 1 Mbit/s |

These are **experimental starting settings**, not measured Tab A8 capacity.
Q0 is low-quality but interactive; below sustainable Q0 keep control alive,
pause video with a weak-link explanation, and offer USB. Do not buffer
indefinitely. A 2.4 GHz label is not a throughput guarantee.

Lower bitrate within the current dimensions first; sustained decoder
overload/thermal pressure lowers FPS, then encoded resolution. Scale encoded
output only, not the OS virtual monitor: avoid window rearrangement and
changes to saved per-host display preferences. Tablet maps input back to
the unchanged session display using existing scaling conventions. Q0 below
host virtual-display minimums is an **encoded** size, not a request to create
a 640x400 OS monitor. Native helper fixed-size limitations can require
Wi-Fi-only FFmpeg fallback; never change USB capture selection to enable it.

Initial hysteresis proposal: down after two congested windows within 1 s
or a hard stall; >=2 s between ordinary rung changes; up one rung only
after 30 s stable headroom (>1.5x target), low queue age and RTT near baseline;
60 s probe penalty on relapse, longer after repeated relapse. Keep IDR/
codec transitions separate from display lifetime. Thermal/memory downshift
overrides quality probing, but radio starvation and slow decoder are
diagnosed separately. Tune from impairment/hardware results, not a universal
"queue >=8" inherited USB policy.

## 9. Android API, lifecycle and power contract

| Concern | API 26-28 | API 29-32 | API 33-36 / newer |
| --- | --- | --- | --- |
| TLS | Secure TLS 1.2 ECDHE/AEAD; no platform TLS 1.3 assumption | TLS 1.3 preferred | Same; provider capabilities checked |
| Wi-Fi lock | `WIFI_MODE_FULL_HIGH_PERF` only during actual Wi-Fi stream | `LOW_LATENCY` available API 29, effective only foreground/screen-on | HIGH_PERF deprecated/API 34 mapped to LOW_LATENCY restrictions; no background guarantee |
| NSD | Tablet service registration/unregister; selected-network UDP fallback if registration routing is unsuitable | Same; no newer APIs without gates | Network-scoped registration where supported, guarded by its actual API level; host performs browsing/resolution |
| Hotspot/P2P | On-demand fine location and required system location state for relevant operations | Same until 32; newer join APIs gated | `NEARBY_WIFI_DEVICES` for applicable hotspot/P2P APIs, `neverForLocation` only if true; location mode can still matter for P2P discovery |
| Basic LAN NSD/TCP | Normal network permissions, no blanket location prompt | Same | Not automatically a nearby-Wi-Fi permission just because sockets use Wi-Fi; Android 16 local-network protection opt-in and API 37 targeting changes separately handled |
| Foreground execution | Notification channel, start FGS from visible UI | FGS restrictions on modern targets; foreground start | POST_NOTIFICATIONS API 33 for drawer visibility; API 34 FGS type/permission requirements |
| Decoder/display hints | Existing compatibility ladder; omit unsupported hints | API 30+ low-latency/frame-rate hints gated | Same; OEM rejection must fall back gracefully |

Declare only needed normal permissions: internet, access network/wifi state,
network-state changes for selected-network operations, multicast-state and
WAKE_LOCK if the chosen Wi-Fi lock requires it. Direct-mode Wi-Fi changes
also require CHANGE_WIFI_STATE. Basic same-LAN operation must not request
fine location, nearby Wi-Fi, camera or battery exemption at startup.
Camera is requested only on "Scan host QR"; hotspot/P2P permissions only
when that feature is selected. Denial leaves manual pairing/LAN/USB usable.

A Wi-Fi-specific foreground service owns session sockets, selected network,
codec lifecycle and locks only while a user-initiated Wi-Fi session is
connecting/active. Use `connectedDevice`, not an indefinite `dataSync`
service to evade modern time limits. On API 34+ declare
FOREGROUND_SERVICE_CONNECTED_DEVICE and satisfy the documented network/
multicast permission prerequisite. The tablet is receiving a display, not
performing Android MediaProjection; QR camera use stays in the activity.
Start from visible user interaction/approved connection flow, call
`startForeground` promptly (within platform deadline), and provide a channel
notification showing host, reconnect status and Stop.

Notification permission denial on API 33+ does not itself prohibit an FGS,
but its notification still must be supplied and system disclosure remains.
Do not create repeated permission prompts or misleading "always connected"
promises after force-stop/task-manager stop.

Screen-on foreground: keep the display awake as today and acquire the
version-appropriate Wi-Fi lock only after Wi-Fi selection. Multicast lock is
for a bounded discovery window, released on success/timeout/stop, never the
entire stream. Lock acquisition failures produce diagnostics and can
continue without a performance guarantee, not silently claim low latency.

Background/screen-off default: stop Wi-Fi video, release codec/high-performance
locks, cancel active input, and close or retain only a bounded short control
grace period according to an explicitly selected keep-alive policy.
Background keep-alive is opt-in; if requested, the FGS can retain control/
monitor state but must not decode a hidden 60 fps stream without purpose.
The default USB background policy/headless sink must not be changed.
This is a Wi-Fi policy, not a refactor of USB activity lifecycle.

FGS and WifiLock do not disable Doze/App Standby or Samsung restrictions.
Avoid an indefinite CPU wake lock; use short bounded holds only for teardown
if demonstrated necessary. On measured Samsung interruption, explain how to
allow background use/remove the app from sleeping/deep-sleeping lists.
Request a battery-optimization exemption only for an opted-in persistent
session after evidence it is required, with consent/policy compliance;
never automatically change system settings. Foreground screen-on use should
not need this prompt.

Handle network callbacks (`onLost`, link-address/capability change), roaming/
band steering and host sleep as connection generations: stop old writes,
invalidate tickets/clock estimates, release old buffers and bind new sockets
to the new Wi-Fi Network. BSSID changes without IP change may briefly stall
TCP; invoke the progress watchdog rather than restarting the whole app.
Do not persistently pin a BSSID or change saved Wi-Fi settings without user
consent. Reconnect after host resume/IP change using stored keys and fresh
discovery, not stale certificate trust.

For Android 16/API 36 test the local-network protection opt-in. Current
official guidance describes mandatory ACCESS_LOCAL_NETWORK for apps
targeting API 37/Android 17; recheck released SDK/runtime behavior when that
target is adopted. Denial/revocation must explain network access, not say
"wrong password". This project currently compiles/targets 36; no unguarded
37-only symbols are assumed.

## 10. Host networking and platform operations

Host browses and dials tablet endpoints only after explicit opt-in, binding
outbound sockets to selected interface addresses. Tablet binds its two
listeners to the selected Wi-Fi addresses, not wildcard by default.
Exclude loopback for LAN, down devices,
docker/bridge/veth/WSL/VPN interfaces by default unless selected. A wired NIC
on the same LAN is a valid first choice. Route-to-tablet checks and interface
metadata guide selection; private RFC1918 address alone is insufficient.
Show NIC/address/band and an override limited to eligible on-link LAN
interfaces, not an off-LAN VPN/overlay. Respect VPN lockdown; if local
traffic is blocked, explain and fail rather than bypass corporate policies.

Tablet creates both input/video listeners successfully **before** advertising.
If either bind or advertisement fails, unwind that Wi-Fi startup generation
and show which component failed; USB remains live. Address changes close
old listener/advertisement generations, open selected new ones and update
discovery; no stale wildcard listener remains.

Initial tablet ports: TCP 37183 input/control/enrollment, TCP 37184 video;
UDP 37182 bounded discovery replies. Host needs outbound TCP to those
ports, UDP discovery and mDNS reception, not inbound Wi-Fi session listeners.
UDP 5353 mDNS is scoped to selected NICs. No router port forwarding,
UPnP, public internet reachability, remote settings UI or shell endpoint.
Report collisions with an action to select new ports; update invitations
and advertisements consistently.

Support IPv4 and IPv6 explicitly with per-address tablet listeners; do not depend
on OS dual-stack wildcard differences. Preserve scope IDs for IPv6
link-local addresses and choose the local interface, not a remote numeric
zone index copied from a QR. Prefer reachable authenticated addresses with
bounded staggered address attempts **within one Wi-Fi session**, not races
across USB/Wi-Fi channels. Broadcast has no IPv6 equivalent; multicast or
QR/manual unicast covers IPv6-only networks.

Firewall doctor distinguishes rule inspection from reachability. On Linux,
detect ufw/firewalld/nftables presence and selected interface/zone; inspect
only with permitted access. Provide exact narrow port/interface/local-subnet
rules or apply app-tagged rules with explicit privileged consent. Never
disable the firewall, flush rules, run arbitrary shell from UI input, or
alter another manager's table. Remove only app-owned rules on rollback.

On Windows, inspect outbound TCP policy and multicast/discovery reception;
do not open inbound host video/input ports for this host-initiated design.
Request consent only for necessary scoped program/UDP rules on a selected
Private profile and local subnet. Public networks need a separate warning/
opt-in or Wi-Fi remains disabled. VPN and multiple profiles require actual
interface checks. Do not equate "rule exists" with "reachable." X11,
Wayland, Windows display backend health appears separately from network
health, so a PipeWire permission failure is not diagnosed as Wi-Fi loss.

Desktop-session execution stays unchanged: Linux needs user session/D-Bus,
Windows capture/input needs an interactive desktop, not session-0 service.
Host lock uses existing privacy/blackout/input policy; Wi-Fi must never
provide remote logon or stream a protected lock screen.

## 11. UX, diagnostics, reconnect and USB priority

### State machine

Host: `DISABLED -> SELECT_NETWORK -> DISCOVER_TABLETS -> SELECT_TABLET -> NEED_PAIRING ->
AUTHENTICATE -> NEGOTIATE -> ATTACH_VIDEO -> STREAMING`.

Tablet: `DISABLED -> AVAILABLE -> INCOMING_HOST -> NEED_PAIRING ->
AUTHENTICATE -> NEGOTIATE -> STREAMING`.

Paired devices skip NEED_PAIRING. Any state can enter
`USB_ACTIVE`, `RECOVERING`, `PERMISSION_REQUIRED`, `REVOKED`,
`INCOMPATIBLE`, or `STOPPED`. One session generation owns the codec, input
writer and host virtual display. Host remembers its preferred paired tablet
and reconnects automatically while it is available. Multiple tablet choices
require selection, not trusting whichever advertised name arrives first.
Tablet accepts only its approved host policy and never auto-enrolls an
unknown host; one active host owns it.

Proposed actionable messages:

| Evidence / failure | Message and action |
| --- | --- |
| No Wi-Fi or lost association | "Connect to Wi-Fi, or use USB." Open network settings; no radio toggling without consent. |
| No internet validation | "This connection works without internet. Keep this Wi-Fi connection." |
| No service discovered | Host: "No tablet found yet. Open DrawSplay and enable Wi-Fi availability on the tablet." Offer manual tablet address; show discovery timeout, not a false isolation diagnosis. |
| Unicast works, discovery fails | "Your network blocks automatic discovery. This connection can still work." Remember authenticated endpoint, explain multicast. |
| Unicast timeout/refusal | Host: "Cannot reach the tablet." Check tablet availability, selected NIC, firewall, guest isolation and port; state which checks were observed. |
| Pin/key changed | "This computer's identity changed. Re-pair locally if you expected this." Never auto-accept. |
| Pairing expired/wrong/revoked | Explain specific locally known state and how to reopen pairing; no screen/input before re-pair. |
| Unsupported version/codec | "Update host/tablet app" with versions/capability conflict; no endless retry. |
| Weak or overloaded link | "Wi-Fi is slow; lowering picture quality." Show rung/queue/RTT, offer USB or 5 GHz when actually available. |
| No suitable direct mode | "This Wi-Fi adapter cannot create a hotspot." Offer tablet hotspot, existing/portable AP or USB. |
| OS permission or VPN restriction | Name the permission/policy and Settings action; USB remains available. |
| Decoder/capture/display failure | Distinguish codec reset, encoder missing, compositor permission and mirror fallback; do not blame all black frames on Wi-Fi. |
| Background/screen-off pause | "Wi-Fi display paused to save power." Restore from visible app; show keep-alive choice separately. |

Doctor on host and tablet exposes: transport/session generation, authenticated
host/device label (redacted export), selected interface/Network, IP family,
Wi-Fi band/RSSI/link-rate if permission permits (unknown otherwise),
discovery stage/result, TLS protocol/provider/cipher, RTT and uncertainty,
delivery throughput, loss/retransmission **source or unknown**, queue bytes/
age/depth, stale/decode/pacing drops separately, encoder/decoder, quality rung,
thermal/memory state, reconnect reason and last successful stage.

Optional debug overlay samples pre-existing counters at low frequency with
bounded storage; logs are structured/rate-limited and never block frame/input
threads. Raw input contents, screen data and trust secrets are excluded.
An overlay is not always-on benchmark instrumentation for USB.

Retry paired Wi-Fi with jittered 0.5, 1, 2, 4, 8 s backoff, maximum 10 s;
reset only after 30 s stable. Network-change/user actions can trigger one
immediate attempt. Pairing/permission/revocation/version failures require
action rather than repeated auth storms. Stop work after user Stop/force-stop.

### Handoff rules

USB wins by default, but **physical attach is not readiness**. Wait for
usable AOA handshake/permission (or explicitly enabled trusted ADB path).
Then quiesce Wi-Fi: cancel/lift contacts, invalidate Wi-Fi session/tickets,
stop its video writer/decoder, serialize host display ownership teardown,
and activate the normal USB session. Wi-Fi must never restart or close a
USB-owned session through a global hook.

When USB drops, reconnect over Wi-Fi only if enabled, paired and permitted;
start fresh input state, not replayed drag/keys. Only one transport can
inject at a time, even during a race. Prefer existing display grace/reuse
where safe; Windows/display backends may still tear down/recreate and move
windows. Promise **automatic reconnect**, not gapless/seamless visual
handoff, until each backend proves it. If selection/ownership changes could
affect ordinary USB startup or reconnect, stop for explicit approval.

## 12. Phased implementation and approval gates

The owner authorized starting implementation after integrating local main.
`git merge --ff-only main` confirmed it already matches the wireless branch
source baseline. Measurement preparation is implemented first in
[`tools/usb_baseline/`](../tools/usb_baseline/README.md), independently of
the running host/Android apps. It collects resource evidence, not the
still-missing full optical/input/power acceptance benchmark. Subsequent
phase gates below remain binding.

Each item is a small reviewable commit or bounded group, with no Copilot
attribution trailer. Do not push/publish as part of discovery.

| Gate / phase | Work after approval | Required evidence before stopping for review |
| --- | --- | --- |
| Phase 0, current | Source discovery, this design, passive evidence only | Owner reviews design, baseline gap, proposed exceptions/decisions. No implementation. |
| Measurement preparation, proposed | External repeatable USB harness; provenance/artifact preservation; independent crypto/provider probe; no product hot-path changes | Full USB baseline on Tab A8, measurement overhead/uncertainty, crypto data, approval of sensitivity/margins. Block Phase 1 without it. |
| Phase 1A | Feature-gated independent Wi-Fi session entry/ownership, test fakes and zero-cost disabled path; leave USB byte path unchanged | Full USB baseline rerun + allocation/profiling comparison and startup/reconnect checks; explicit review of any shared edit. |
| Phase 1B | Tablet advertisements/listeners, host discovery/dialing, LAN-only destination enforcement, authenticated-code PAKE enrollment, backup QR, TLS/mTLS and channel association, doctor | Negative auth/pairing/off-link-route tests before screen/input; required PAKE/API-26 TLS-role interoperability and mDNS dependency review. Full USB rerun. |
| Phase 1C | Bounded TCP Wi-Fi AU/input/control paths, fixed initial quality, progress watchdog, reconnect, network binding, Wi-Fi FGS/locks and overlay | LAN video/input works at API 26 and Tab A8; full requested metrics, memory/provider allocations, no USB regression. Stop for Phase 1 approval. |
| Phase 2 | Approved host/tablet AP modes and platform fallbacks; quality ladder/jitter/IDR recovery; optional separately approved USB enrollment | Per-platform offline connectivity and lifecycle tests, USB bootstrap overhead evidence if selected, full USB rerun. Stop for approval. |
| Phase 3 | Executable netem/software harness, fuzz/security/soak/lifecycle/environment matrix, docs and final report | Actual automated logs plus hardware results, full USB before/after tables, known limits, no unexplained failures. Stop for release approval. |

No code is authorized by a speculative perf target. If bounded Wi-Fi work
needs a new shared decoder/capture API or exceptions to allocation rules,
bring the exact diff/design back before writing it.

## 13. Testing and hardening plan

This section specifies future implementation; these harnesses/tests were
**not built or run in Phase 0**. The existing tests are useful protocol/
display/unit coverage, not hardware USB/Wi-Fi performance certification.
No tracked benchmark/fuzz suite currently supplies the full requested gate.

### 13.1 Automated impairment

Build a rootless deterministic software link harness for Go/JVM CI with
injectable clock, bounded chunks/buffers, seeded loss/jitter/reorder/duplicate/
bandwidth/burst/drop schedules. Model raw framing/datagrams separately from
TCP: arbitrary byte reordering/corruption is not what TCP delivers.
TLS integration over real loopback tests actual handshakes.

Build a Linux namespace/veth bridge router controlled by a scoped test tool:
`tc netem`/TBF in both directions, including host egress and return path.
Create only named app-owned namespaces/interfaces/qdiscs, capture seed and
effective configuration, require consent for CAP_NET_ADMIN, and clean up
those exact resources. Never impair the user's default NIC or active USB
session. Windows/Android sockets route through the test AP/router for
hardware runs. Pure CI is a logic bound, not a radio/power benchmark.

Initial acceptance **targets, not results** (delay is one-way per direction;
E2E is synthetic-source to photons, input RTT is application-observed):

| Profile | One-way delay / jitter / loss / cap | E2E p95 ceiling | Input RTT p95 ceiling | Required useful FPS |
| --- | --- | --- | --- | --- |
| Clean LAN | 5 ms / 2 ms / 0% / 30 Mbit/s | 160 ms | 40 ms | >=55 at Q3/60 |
| Busy LAN | 20 ms / 10 ms / 1% / 8 Mbit/s | 300 ms | 150 ms | >=27 at Q2/30 |
| Weak 2.4 GHz model | 50 ms / 20 ms / 3% / 2 Mbit/s | 500 ms | 300 ms | >=18 at Q0/20 |
| Severe but usable model | 100 ms / 40 ms / 5% / 1.5 Mbit/s | 800 ms | 600 ms | >=15 at Q0/20 |
| Beyond service envelope | 200 ms / 50 ms / 10% / 1 Mbit/s, bursts/reorder/duplicates | No smoothness claim | No low-latency claim | Bounded memory; warn/pause and recover, never hang |

Also sweep 0/0.1/1/3/5/10% loss, 5-200 ms delay, variable jitter,
0.5/1/2/4/8/20/50 Mbit/s, correlated burst loss, duplication/reordering
on the IP path, and 1/5/30 s link outages. At usable profiles: unique-frame
late/drop rate <=5% clean and <=10% busy, measured by source IDs, not NAL
counters. Below-envelope pauses are explicit failures to provide video,
not discarded from the report to improve FPS.

After link restoration: authenticated control re-established <=5 s,
decodable fresh video <=8 s, no replay/stuck contacts/codec hang. A
progress watchdog detects stalled decode <=1 s, resets/re-IDs <=3 s on a
restored usable link, with bounded retries; otherwise show a clean failure.
For every profile, queues never exceed negotiated byte/slot limits.
Post-warm-up PSS must show no increasing leak trend; target slope <=1 MiB/h,
end PSS within 10 MiB of start after equivalent idle/GC state, and the
validated total-device/codec budget. Failure blocks acceptance, not hidden
GC invocation during normal sessions.

### 13.2 Protocol robustness and security tests

Add Go fuzz targets and Kotlin/JVM property/fuzz corpus tests for Wi-Fi
envelope lengths/types/epochs, mux/legacy bounds where exercised by
unchanged USB, AU boundaries, capability JSON and TLS enrollment/attach
state machines. Use shared cross-language golden records and max-size
corpora; fuzz short reads/writes, truncated data, huge lengths, unknown
versions, reorder, duplicates, stale tickets and sequence wrap.

Assertions: no crashes, panics/OOM, unbounded allocation, hanging workers,
pre-auth injector/capture/display calls, or silent downgrade. Tests that
demonstrate pre-existing unrelated USB bugs are recorded and escalated, not
opportunistically fixed in this branch.

Negative integration tests: unauthenticated and self-signed unpaired peers,
wrong host pins/client keys, expired/wrong-use certificates, revoked devices,
replayed auth/control/tickets, mTLS device mismatch across channels, changed
key on resumed session, numeric PAKE wrong code if selected, invitation
MITM/expiry/race, disabled pairing, port scan/slowloris load and resource
limits. Verify host state and mocked OS injector call count remain zero
until auth succeeds. Plaintext and old insecure Wi-Fi clients fail closed.

An independent formal security review is a later release gate; a test list
and TLS choice are not a completed review.

LAN-confinement tests capture app-owned traffic at the test router's LAN
and WAN interfaces, with internet both present and absent. Assert zero
DrawSplay WAN packets/public DNS queries and no external certificate,
telemetry or pairing dependencies. Inject off-link IPv4/IPv6 discovery and
manual/QR endpoints, private addresses reachable only through a gateway,
VPN/default-route changes, malicious endpoint hints and Wi-Fi loss/mobile
fallback. All must be rejected or pause cleanly, never route elsewhere.
Test host-originated TCP on both channels and tablet NSD registration from
API 26 onward. Confirm tablet never browses for hosts, discovery alone
never enrolls a host, and repeated connection attempts cannot keep an
unavailable tablet awake indefinitely.

### 13.3 Soak and lifecycle

Run >=8 h on the physical Tab A8 in both USB and Wi-Fi, cool and warm
environments, fixed recorded brightness/content/charging conditions.
Collect low-cadence PSS/RSS/native/graphics/swap, CPU, thermal status,
frequency/throttle evidence where readable, calibrated power proxy, FPS,
latency tails, FD/socket/thread counts, decoder generations and lock
ownership. A harness failure/gap invalidates the corresponding claim.

Manual/automated lifecycle matrix: screen off/on; foreground/background;
Doze/App Standby; Samsung sleeping list; airplane mode; Wi-Fi off/on;
roam/band steering/DHCP/IP changes; host suspend/resume; tablet/host reboot;
app kill/force-stop/relaunch; permission revocation; USB attach/unplug
both ways mid-session; fast connect/disconnect (>=100 cycles).
Use test-device-only automation and consent for disruptive actions.

For every error/transition record: user-visible message/action, ownership,
all contacts lifted, finite retry, all superseded sockets/threads/codecs
closed, locks released, and no zombie virtual monitors. Confirm normal
display teardown/grace and watchdog behavior on each backend; do not just
assert the process exits.

### 13.4 Environment matrix and hardware checklist

| Axis | Required coverage / evidence |
| --- | --- |
| Android | 8/API 26, 9/28, 10/29, 11/30, attached 14/34 and newest available OS/SDK at test time; emulator for logic only |
| Hosts | Linux X11, GNOME Wayland, KDE Plasma 6 Wayland; sway/Hyprland where supported; Windows 10 and 11 physical interactive sessions |
| Radio/network | Real 2.4/5 GHz; WPA2/WPA3 with API-26 WPA2 fallback; crowded links; multicast blocked; client isolation; no-internet hotspots; AP+STA allowed/disallowed |
| Routing/firewall | ufw/firewalld/nftables/Windows firewall enabled and controlled rules; VPN/multiple NIC/virtual devices; IPv4-only, dual-stack, IPv6-only/link-local scopes |
| Devices/input | Real Tab A8 for memory/thermal/power and touch; separate active-pen-capable hardware for pressure/tilt/buttons; no claim that a Tab A8 can emulate absent digitizer hardware |

Hardware-only checklist before release: physically pair and revoke; observe
QR/code failures; confirm no-internet association; move tablet across APs;
use real touch/pen responder and optical rig; verify source/visible frame IDs;
measure battery with stated instrument; perform soak; inspect displays after
all disconnects; inspect notification/locks after user Stop; repeat the
unchanged full USB baseline. Record OS/build/driver/AP/firmware and
limitations for every cell, including NOT TESTED rather than treating an
emulator result as physical evidence.

### 13.5 Existing commands and future docs

Use existing host `go vet ./...`, `go test ./...`, platform-target builds and
race tests; Android `:app:compileReleaseKotlin` and
`:app:testReleaseUnitTest` with JDK 17 and the actual Gradle SDK requirements.
Check CI workflows with actionlint and added shell tools with `bash -n`.
Do not install dependencies merely to create Phase 0 evidence.

With implementation, update README setup/use, FUNCTIONALITY protocol/
capabilities, BUILD prerequisites, privacy/security descriptions of local
listeners/trust, and relevant wiki transport/security/operations pages.
Add setup, troubleshooting and doctor guides and an executable QA checklist;
do not turn this unimplemented design into claims about the shipping app.
Final report must link raw benchmark evidence, before/after USB and Wi-Fi
tables, tested matrix cells, failures and unverified platform limits.

## 14. Risks and open approval questions

| Risk | Mitigation / stop condition |
| --- | --- |
| No full baseline; installed build differs | Preserve artifacts/provenance and run approved harness before code; no zero-regression claim from snapshots |
| New shared code silently affects USB | Connection-time seam only; document each shared edit; rerun full baseline and stop on adverse evidence |
| Provider/frame allocations violate hard rule | Profile actual TLS/codec and bounded ownership; if strict provider-wide zero cannot be met, block, do not assume exception |
| TCP HOL on congested links | Separate input socket, queue age/watchdog, IDR/video-only reset, safe quality floor; hybrid only after failure evidence |
| Authentication/pairing complexity | Required vetted PAKE for primary code enrollment, backup pinned QR, per-device mTLS, no numeric bearer code, formal review and negative tests |
| AP/P2P hardware/OEM/Windows offline limits | Capability test; ordinary station/tablet AP/portable AP/USB fallback; explicit unsupported message |
| Memory pressure/Samsung lifecycle | Small fixed pools, measured codec/native PSS, service/lock lifecycle and low-cadence power testing; no absolute LMK guarantee |
| USB/Wi-Fi race or stuck monitor/input | Session generation + authenticated association + serialized handoff + cancel-all + backend lifecycle testing |
| Benchmarks disturb USB | Profiler-off controls, external timing, separate synthetic desktop, log sampler overhead and incomplete trials |
| New Android permission rules | Version/target gates, denial/revocation tests; reverify API 37 guidance before target upgrade |

Owner decisions required before implementation:

1. Approve the isolated Wi-Fi path and explicitly bounded connection-time
   integration changes rather than a generalized USB transport refactor.
2. Approve a measurement-only preparation step before Phase 1, a maintenance
   window/artifact-provenance strategy, and access to optical/power/input test
   equipment. The attached API-34 Tab A8 cannot prove API-26 radio/power claims.
3. Clarify whether "no new allocations" includes allocations inside
   TLS/MediaCodec providers; approve no exemption implicitly. Approve
   equivalence sensitivity/margins before baseline trials.
4. Accept secure TLS 1.2 on API 26-28, or require a separately measured,
   maintained TLS-1.3 provider there.
5. Confirm proposed one-time host-displayed code entered on the discovered
   tablet using vetted PAKE. Host discovers tablets and initiates TCP;
   LAN-only traffic and backup-only QR are owner requirements, not options.
6. Approve Linux host AP / conditional Windows hotspot / tablet local-only
   hotspot fallback order, with no universally guaranteed Wi-Fi Direct.
7. Approve Wi-Fi-only background pause defaults and automatic-but-not-
   necessarily-gapless USB-priority handoff. USB policy stays unchanged.
8. Decide whether USB bootstrap is in Phase 2; it is off until separately
   approved, including hotspot credential sharing consent.

No need to answer every minor engineering choice: buffer layouts, bounds,
timeouts, quality values and thresholds above are proposed starting points
for measured validation. Changing a hard requirement or a major security/
USB boundary is not delegated to implementation discretion.

## 15. External references and evidence limits

Public platform references consulted 2026-10-22; performance claims require
local hardware evidence, not these documents:

- [Android NSD guide](https://developer.android.com/develop/connectivity/wifi/use-nsd)
  and [NsdManager API levels](https://developer.android.com/reference/android/net/nsd/NsdManager).
- [Network socket factory and lifetime](https://developer.android.com/reference/android/net/Network)
  and [ConnectivityManager](https://developer.android.com/reference/android/net/ConnectivityManager).
- [Android local-only hotspot](https://developer.android.com/develop/connectivity/wifi/localonlyhotspot),
  [Wi-Fi Direct permissions and limitations](https://developer.android.com/develop/connectivity/wifi/wifip2p),
  [Wi-Fi Aware hardware-dependent support](https://developer.android.com/develop/connectivity/wifi/wifi-aware).
- [WifiManager lock semantics/API gates](https://developer.android.com/reference/android/net/wifi/WifiManager).
- [Android 10 TLS 1.3 behavior](https://developer.android.com/about/versions/10/behavior-changes-all#tls-1.3)
  and [TLS security guidance](https://developer.android.com/privacy-and-security/security-ssl).
- [Foreground service connected-device requirements](https://developer.android.com/develop/background-work/services/fgs/service-types#connected-device).
- [Local-network protection and target SDK changes](https://developer.android.com/privacy-and-security/local-network-permission).
- [NetworkManager nmcli hotspot capabilities/security](https://networkmanager.dev/docs/api/latest/nmcli.html)
  and [device capability examples](https://networkmanager.dev/docs/api/latest/nmcli-examples.html).
- [Windows tethering manager/API capabilities](https://learn.microsoft.com/en-us/uwp/api/windows.networking.networkoperators.networkoperatortetheringmanager)
  and [desktop Wi-Fi Direct pairing requirements](https://learn.microsoft.com/en-us/windows/win32/nativewifi/using-the-wi-fi-direct-api).

The current discovery does not verify Windows/GNOME/KDE Wi-Fi, API-26 real
radio behavior, routerless operation, cryptographic throughput, end-to-end
latency, active-pen fidelity, or an 8 h soak. These are explicit release
gates, not assumed successes.
