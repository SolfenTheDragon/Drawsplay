# DrawSplay Functionality Reference

This document defines current behavior, target behavior, and implementation boundaries for the project.

## 1. Product Goal

Use a tablet client as a **secondary display** for a desktop host (Windows and Linux), with **touch and pen input returned to the host**. Android is implemented today; host protocol now carries generic client identity fields to support future Windows/Linux tablet clients.

## 1.1 Latency Targets (Priority Requirement)

Target profile for pen/touch interactions:

1. **Pen input end-to-end latency:** p95 `< 20 ms`.
2. **Transport latency (tablet -> host) for input packets:** p95 `< 5 ms` over USB.
3. **No backlog growth during sustained drawing:** stale move events should be dropped in favor of latest state.

## 2. Current Implemented Functionality (Starter)

### 2.1 Host service (`host/`)

- Cross-platform Go service with two localhost TCP listeners, reached by default over USB through Android Open Accessory (AOA); the legacy ADB reverse transport is optional:
  - `127.0.0.1:27183` — input channel (tablet → host).
  - `127.0.0.1:27184` — video channel (host → tablet).
  - Plus `127.0.0.1:27180` — settings web UI (local browser only, not tunneled).
- Optional Wi-Fi mode (disabled by default) adds LAN listeners and DNS-SD advertisement (`_drawsplay._tcp`), with Android NSD-based endpoint discovery/fallback while preserving the USB-first behavior path.
- Android pinned Wi-Fi TLS connections authenticate the peer before returning a socket to input/video channels. Pins must be 64 hexadecimal SHA-256 characters; connect timeouts must be positive. TCP connect and TLS handshake reads have the configured timeout, failed setup closes the socket, and successful setup clears the handshake read timeout for the channel to configure. `TCP_NODELAY` is enabled for these TLS sockets. This does not change USB or the wire format.
- `TlsSocketFactory.authenticateAccepted` also authenticates an already-connected socket as a TLS client, allowing a host-initiated TCP connection while retaining the host's pinned TLS server identity. It takes ownership of the supplied socket and closes it on setup failure. This primitive is not yet wired to tablet advertisement/listening or the planned pairing flow.
- A separate development command, `host/cmd/wifi-discover`, browses tablet advertisements (`_drawsplay-tablet._tcp.local.`, `v=2`) on an explicitly chosen IPv4 LAN interface, validating on-link addresses and bounded DNS/TXT/cache sizes. It reports only untrusted candidates and never dials, pairs, captures or injects input. It is not run by normal host startup. See `docs/wifi-discovery.md`; the integrated host-discovered session remains work in progress.
- Does not start or resolve ADB by default. If the optional `adbTunnelEnabled` / `-adb-tunnel` transport is enabled, the host maintains `adb reverse` rules for both ports in a background retry loop (`2s` base, bounded exponential backoff with jitter, `5s` per-command timeout). Devices are enumerated and rules applied per-serial (`-s`). `preferredADBSerial` / `-adb-serial` can restrict tunnels to one device; `requirePreferredADB` / `-require-adb-serial` enforces strict device trust. Unauthorized and Linux `no permissions` states are surfaced in setup warnings.
- Optional `autoLaunchAndroidApp` / `-auto-launch-android-app` launches `com.drawsplay.tablet/.MainActivity` for newly connected devices when the selected transport supports automatic launch.
- Wi-Fi transport controls (`wifiEnabled`, bind/ports/service-name/TLS/auth-token/max-bitrate) are startup configuration; USB transport behavior is unchanged unless Wi-Fi is explicitly enabled.
- An internal mux frame codec is implemented for the planned custom USB transport layer (`magic/version/stream/flags/seq/len/payload`) so input/control/video/stats can share one future USB session with explicit stream IDs and policy-aware queueing.
- **Virtual display (extend mode)**: on video-client connect the host creates a per-session virtual monitor and captures it; on disconnect the monitor is removed. Display setup that fails transiently (topology still settling after the previous session's teardown, monitor hot-plug) is retried up to 5 times at 500 ms before the session is abandoned. Flags `-width`/`-height` (default 1920×1080) request its size when host-managed resolution is active.
  - **Windows**: DrawSplay's own IddCx indirect display driver (source in `drivers/windows/idd`, bundled into the host binary with `go:embed` and installed natively via `pnputil` staging + SetupAPI devnode creation — no network download, no third-party driver). It is controlled via IOCTLs (add/remove monitor). The driver advertises a single mode — the tablet's native resolution at the tablet panel's refresh rate (`tabletPanelHz`, defaulting to 120 Hz when the client reports none), marked preferred — so the monitor comes up at exactly that mode and the virtual display therefore defaults to the tablet's refresh rate automatically. After plugging, the host activates it with `SetDisplayConfig(SDC_TOPOLOGY_EXTEND|SDC_APPLY)`; there is no custom-resolution registry key, no adapter reload and no keepalive ping (closing the control handle removes the monitor). The new monitor is located by diffing DXGI outputs before/after plug; its adapter/output indices feed `ddagrab` and its desktop rectangle feeds input mapping. Capture fps follows the live Windows display refresh, capped at the tablet panel rate. If the driver is absent (e.g. a dev build without the embedded files) the host falls back to mirroring the primary display, so video always works. The driver installs on Windows 10 1903 (build 18362, IddCx 1.4) and later. It is GPU-vendor neutral: it creates its swap-chain device on whichever render adapter IddCx assigns (NVIDIA, AMD, Intel down to Direct3D feature level 9.1, or the Microsoft Basic Render Driver), retries device creation briefly while a GPU driver is updating or resetting, and hands a failed swap chain back to the OS so it is reassigned instead of freezing the monitor. Until IddCx reports the adapter initialized, add-monitor returns "not ready" and the host retries for up to 5 s.
  - **Linux (X11)**: a virtual RandR output (`vkms`'s `Virtual-1-1`, or `DRAWSPLAY_X11_OUTPUT`) switched on with `xrandr` as an extended display next to the other monitors (`x11_extend_linux.go`); without one the X screen is mirrored. If vkms makes its virtual output available after startup, the mirrored session detects it and restarts into extended mode; the Web UI setup repair checks for the same condition. After a session ends the output is switched off after a 5 s grace period; at host start, a virtual output a previous host process left on (crash, kill, suspend) is switched off; a failed switch-off is retried every second, and the output is re-checked for a few seconds afterwards and switched off again if the window manager brought it back on. On X11 sessions without a virtual output, the host loads `vkms` (`modprobe vkms create_default_dev=1`, falling back to a plain load) at start and whenever a device is newly connected (`vkms_setup_linux.go`): as root or via passwordless sudo (`sudo -n`); at host start it never prompts, and when a tablet connects (ADB reverse ready or USB accessory opened) it may show one polkit (`pkexec`) password prompt that also writes `/etc/modules-load.d/drawsplay-vkms.conf` and `/etc/modprobe.d/drawsplay-vkms.conf` so vkms loads at boot and the prompt is not needed again. A dismissed or failed prompt is not repeated for 2 minutes; nothing is done when `DRAWSPLAY_X11_OUTPUT` is set or vkms is already loaded without a visible output (a new X session is needed). Existing mirrored sessions restart onto the extended display once the output appears.
  - **Linux (GNOME Wayland, Ubuntu 22.04+)**: Mutter ScreenCast D-Bus API (`CreateSession` → `RecordVirtual` → `Start`); the compositor plugs a virtual monitor and exposes it as a PipeWire node (`mutter_extend_linux.go`). It behaves like the X11 extended display:
    - `RecordVirtual` declares the standard resolutions, the configured one (preferred) and the tablet's native one, so resolution and position are OS-managed from Settings → Displays.
    - Mutter has no stored configuration for a set of monitors that includes a virtual monitor (each one gets a new serial), so it lays every display out again in a row at default settings. The host reads `DisplayConfig.GetCurrentState` and applies one temporary configuration (never saved to `monitors.xml`): the other displays as they were (position, mode, scale, transform, primary, layout mode, DRM leases, underscanning, colour mode, RGB range) and the virtual monitor at the configured resolution where the user last had it (side and offset), else to the right. Layouts are checked against Mutter's verification rules (supported scales, one primary, origin at 0,0, no overlap, edge-connected) before being applied, and re-planned when the monitors change in between.
    - Touch/pen input is mapped to the monitor through GNOME's per-tablet "Map to Monitor" setting.
    - A `MonitorsChanged` watcher follows moves without a restart (capture follows the monitor) and restarts the session when the monitor's resolution changes (saved as the configured resolution unless GNOME chose the mode itself after a hotplug), when it starts mirroring another display, or when it is switched off or removed. Stopping screen sharing from GNOME's indicator removes it; the next session creates a new one where it was. Repeated capture failures replace the monitor (`Recover`).
    - The monitor is kept for 5 s after the client disconnects, so a reconnect reuses it; then, and when the host exits, it is removed. Mutter restores a stored configuration for the remaining displays itself; otherwise it lays them out in a row in its default layout mode, ending leases, and the host puts them back as they were (moved to the origin; the first takes over the primary role if the virtual monitor had it).
    - Unsupported: rotating the virtual monitor (Mutter streams it unrotated) and switching it off in Settings (GNOME Shell 50 crashes on a configuration without it, so the host never applies one).
  - **Linux (KDE Plasma 6, sway, Hyprland)**: a shared manager (`wl_extend_linux.go`) gives these the X11 extended display's behaviour: one virtual output per connection at the configured resolution, placed right of the other displays or where the user last had it (anchored at 0,0 on sway as on X11; Hyprland allows negative positions), kept across quick reconnects, removed after the grace period and on exit (switching the fallback display on when it was the only one), pen mapped to it, followed by a watcher (move: re-target; resize/rotate: save the resolution and restart; disable/mirror/removal: restart; a config reload or hotplug: restart without saving).
    - **sway** (`sway_extend_linux.go`): i3-ipc `create_output` → `output HEADLESS-N enable mode … pos … transform … scale …`, `input 7504:20548:Drawsplay_Pen map_to_output`, `output … unplug` (1.9+, else `disable` + adoption via `$XDG_RUNTIME_DIR/drawsplay/sway-output`); captured with wlr-screencopy (`wl_screencopy_linux.go`, raw frames into ffmpeg stdin).
    - **Hyprland** (`hypr_extend_linux.go`): `.socket.sock` `output create headless Drawsplay`, `keyword monitor …` (or `eval hl.monitor(…)` for Lua configs), `device[drawsplay-pen…]:output`, `output remove`; `.socket2.sock` events; captured with wlr-screencopy.
    - **KWin** (`kwin_extend_linux.go`): `zkde_screencast_unstable_v1.stream_virtual_output(_with_description)` creates `Virtual-Drawsplay` and its PipeWire node (captured like GNOME's); the host installs a `.desktop` file with `X-KDE-Wayland-Interfaces` so KWin grants the restricted protocol. KWin lays the output out from its remembered per-name configuration and its mode is fixed at creation, so the resolution comes from the host settings (re-created once with the swapped mode when KWin remembers it rotated); pen mapped with KWin's `org.kde.KWin.InputDevice` `outputName`.
  - **Linux fallback**: the Mutter and KWin paths are only taken when the GStreamer PipeWire consumer tooling (`gst-launch-1.0` + `pipewiresrc`) is runnable; otherwise — and on other Wayland desktops (Cinnamon on Wayland), where the host cannot create a monitor — the host logs the reason and mirrors the X screen via ffmpeg `x11grab`. Under Wayland that is XWayland's screen, which shows only X11 applications (native Wayland windows are black) and is not extended: the host logs a warning, preflight reports a `wayland-compositor` warning, and the first-run checklist shows a **Wayland virtual display** item.
- **Video pipeline**: capture/encode candidates are built per platform and tried in order until one produces output (startup failures fall through; failures after first output end the session).
  - Windows: ffmpeg `ddagrab` with `draw_mouse=1` and `setpts=N/(FPS*TB)`, followed by encoders `h264_nvenc` → `h264_amf` → `h264_qsv` → `libx264`, except that the encoder of the GPU owning the captured output (DXGI adapter vendor: NVIDIA → NVENC, AMD → AMF, Intel → QSV) is tried first. On hybrid laptops the display often lives on the Intel iGPU, where NVENC's zero-copy mapping fails and its readback fallback would otherwise win at a fraction of QSV zero-copy's frame rate. PTS normalization is required because the indirect display can emit duplicate/non-monotonic timestamps that cause hardware encoders to repeatedly encode stale textures while still reporting 60 output frames per second. QSV uses `async_depth=2` to overlap one upload with one encode. The Windows bitrate ladder is capped at 30 Mbps after display-pixel scaling: on the USB accessory link higher rates caused 0.4–1.1 s send stalls (mostly keyframe bursts) at 2960×1848@120, without lowering the selected display resolution or refresh rate. The Android client reports whether its activity is visible; ffmpeg runs at high priority while visible and below normal during background keep-alive.
    - NVENC and QSV probe a zero-copy path first and automatically fall back to the established readback path when the driver/ffmpeg combination does not support it. NVENC maps `ddagrab` GPU frames to CUDA (`scale_cuda=format=nv12`); QSV maps them into a derived QSV device and converts BGRA?NV12 on the GPU (`hwmap=derive_device=qsv,format=qsv,vpp_qsv=format=nv12`, with `dup_frames=1` and rebuilt PTS). A zero-copy startup failure (e.g. "Only fixed-size pools can be mapped to QSV frames" on older ffmpeg) disables only that encoder's zero-copy probe for the host run; it never marks the encoder itself unavailable.
    - Experimental temporal layering mode (`temporalLayering` / `-temporal-layering`) enables NVENC non-reference P-frames and runtime adaptation that first drops non-reference enhancement frames under congestion before forcing a pipeline bitrate-rung restart.
  - Linux PipeWire: `gst-launch-1.0 pipewiresrc path=<node>` with encoders `nvh264enc` → `vah264enc` → `vaapih264enc` → `x264enc` (plus an unpinned-caps software safety net), byte-stream H.264 on stdout. `pipewiresrc` source caps are left unconstrained; width/height/framerate are normalized downstream via `videoscale`/`videorate` instead, since Mutter negotiates variable framerate and pinning source caps can fail negotiation outright on some setups. `videorate` converts the damage-driven stream to constant framerate for the encoder, and `pipewiresrc keepalive-time` (one frame period) re-emits the last frame on a static desktop so the encoder-start timeout cannot trip and the tablet decoder's held pictures are flushed (otherwise the pointer appeared to keep moving a couple of ticks after the user stopped). `videoconvert` is the branch's single-threaded hot spot (~4.7 ns per pixel), so its `n-threads` is sized from the frame's own cost — `ceil(pixels x 4.7 ns x fps)`, capped at `NumCPU/2` and at 4 — which leaves 1080p60 single-threaded (threading it measured no gain and more than doubled capture CPU) and gives 1440p and larger the extra workers they need. The VA-API candidates (`vah264enc`/`vaapih264enc`) need a GPU driver installed (e.g. `intel-media-va-driver-non-free`); without one the host falls back to a conservative software `x264enc` profile that can look artifacted and struggle to hold frame rate under CPU load. Dependency preflight checks `gst-launch-1.0`, the `pipewiresrc` plugin, and `x264enc` (software fallback) and reports targeted remediation warnings when missing.
  - Linux X11 fallback: ffmpeg `x11grab` with the same ffmpeg encoder chain, plus an `h264_vaapi` candidate inserted ahead of the software encoder (Linux only; the shared candidate list and the Windows/blackout pipelines are unchanged). It is offered only when ffmpeg advertises `h264_vaapi`, the build's `scale_vaapi` filter exists and supports the `out_range` option (checked by reading `ffmpeg -h filter=scale_vaapi` output, since ffmpeg exits 0 even for a filter it lacks), a DRM render node exists and the session codec is H.264; colour conversion is done on the GPU (`hwupload,scale_vaapi=format=nv12:out_range=tv`) because converting in software first costs more CPU than encoding with `libx264` outright. If VA-API is present but unusable the candidate fails at startup and the loop falls through to `libx264` as before. `h264_vaapi` runs with `-async_depth 1` (ffmpeg's default of 2 held every finished frame behind the next submission, a frame period at 60 fps) and `-rc_mode VBR` under the shared `-maxrate`/`-bufsize` cap (in CBR the iHD driver pads every frame to the bitrate budget: 48.8 KiB per static-desktop frame at 24M versus ~1.5 KiB). The `libx264` fallback gets an explicit `-threads N` instead of ffmpeg's auto (1.5 frame threads per logical CPU, each one a frame of delay because the tablet requires `sliced-threads=0`; 12 threads measured 188 ms capture-to-stdout at 1080p60): the smallest count whose estimated cost `ceil(pixels x 7 ns x fps)` fits the frame period, capped at `NumCPU/2` and at 4, with the size probed once per display from the X root window (`xdpyinfo`, then `xrandr`; the cap when unknown), so 1080p60 runs single-threaded. Linux vendor-runtime load failures (`Cannot load libcuda.so…`/`libnvidia-encode.so…`, AMF's `libamfrt64.so.1`) count as permanent encoder errors like their Windows DLL counterparts, so a machine without that GPU pays the failed launch (~0.1 s) once per process instead of at every session start. Measurements: `docs/performance/linux-x11-performance-2026-10-07.md`.
  All profiles are low-latency (no B-frames, zerolatency where supported, 2 s keyframe interval). Output is split into Annex-B NAL units and framed to the client. On the Linux `x11grab` H.264 path each frame's last NAL unit is sent as soon as ffmpeg's final write for it is read, instead of when the next frame's start code arrives a frame period (16.7 ms at 60 fps) later: with `-flush_packets 1` ffmpeg writes each access unit as 32 KiB writes plus one shorter write, the stdout pipe is grown to 256 KiB before the encoder starts, and a read is trusted to end on a write boundary only when Linux pipe semantics prove it (`au_boundary.go`, `pipe_linux.go`). If the bytes after an early-sent unit don't open with a start code, the pipeline restarts and early emission stays off for the process. Frames that can't be proven (exact multiples of 32 KiB, roughly 124 KiB and up, or read while the reader lags) keep the one-frame wait. Windows `ddagrab` H.264 pipelines do the same (`pipe_windows.go`): the named-pipe file system queues each `WriteFile` whole, so any read shorter than its 64 KiB buffer ends on a write boundary and only exact multiples of 32 KiB keep the wait. GStreamer and HEVC pipelines use the plain splitter. One video session at a time; the pipeline and the virtual display are torn down when the client disconnects. Flags: `-ffmpeg`, `-fps` (default 120), `-bitrate` (24M at 1080p reference), `-width`/`-height`. On Linux the effective capture rate is clamped to 60 fps on the PipeWire and screencopy backends (Mutter renders the virtual monitor on the compositor's 60 Hz frame clock). The `x11grab` path may run up to 120 fps (`x11HighFPSCeiling`) when the `h264_vaapi` candidate is available: the tablet must ask for it (remembered mode or web UI), the Linux first-connect default stays 60. Only the VAAPI candidate runs above 60; NVENC/QSV/`libx264` candidates stay at 60. A 60 Hz X11 compositor (e.g. Cinnamon) produces no new content above 60 Hz, so the gain is a shorter capture-phase wait (average 8.3 ms at 60 fps, 4.2 ms at 120), not more distinct frames. The VAAPI args carry an explicit `-level 5.2` (without it iHD rejects 1080p above 60 fps) and, above 60 fps, `-r <fps>` because the raw H.264 output carries no timestamps and ffmpeg otherwise encodes every grab (~234 fps measured for a 120 request); `-r` is used rather than an `fps` filter, which would hold a frame. Cost: roughly 2x encode CPU (one ffmpeg thread near 100% at 1080p120 on Intel UHD) and up to 2x bitrate on animated content.
  Older Windows ffmpeg builds (before ~7.x) cannot map dynamic `ddagrab` D3D11 textures into QSV's fixed-size frame pool, so QSV falls back to D3D11 readback to BGRA system memory plus a QSV upload. The readback path caps native-resolution capture near 30 fps on Intel iGPUs (zero-copy measured ~1.7?3? faster at 2736?1824); with readback, 1680×1050 is the default smooth mode, 1920×1200 is a balanced sharper mode, and native tablet resolutions may run below 60 fps.
- **Adaptive bitrate (M4)**: the configured `-bitrate` is a 1080p reference, scaled by the display's pixel count and clamped to a USB 2.0 link budget of 60 Mbps (floor 500 kbps); the result is the top of a ladder of ~60% reductions. Congestion — a single host send blocking > 300 ms, or a client stats report with decode queue ≥ 8 that does not also report stale-frame skips — restarts the pipeline one rung down (new config packet + fresh SPS/PPS/IDR, so the client recovers seamlessly). A deep queue reported together with `staleFrames` is the client draining the backlog of a stall that has already ended (a Wi-Fi roam, a USB hiccup); a lower bitrate would not have prevented it and a restart would only add a second freeze, so it does not count. This applies on **every** backend, including the Linux Mutter/PipeWire path: a restart there keeps the ScreenCast session, the virtual monitor, its PipeWire node and the client's video socket, respawning only the GStreamer process. Because no Linux encoder offers the temporal-drop mitigation that would otherwise absorb a first blip, Linux step-downs (PipeWire and `x11grab`) additionally require two congestion reports within 2 s (the client reports every 250 ms), so an isolated stats sample or one slow send cannot restart the encoder while sustained congestion still walks the ladder down at the normal rate. Windows (`ddagrab`) uses the same two-reports-in-2 s confirmation, also ignores reports during the first 3 s after each encoder (re)start (they describe the restart hiccup, which otherwise cascaded the ladder down to ~0.7 Mbps and a blocky picture), and never steps below a quarter of the ladder top. Windows ffmpeg encoders also get a 125 ms VBV (`-bufsize` = bitrate/8) instead of the shared ~1.25-frame buffer, which at high refresh rates starved keyframes and made the picture blocky every 2 s and after each restart (the same fix as Linux `x11grab`). Step-downs are rate-limited to one per 5 s; a rung is probed back up only after 60 s of clean streaming, and a rung that congests within 15 s of being probed is held off for 5 minutes (anti-oscillation). Restarts reuse the last known-good encoder first. **Decoder-bound detection**: if two consecutive queue-only step-downs (no slow socket write) each fail to stop the congestion while the stream sends under half of its rung, the tablet's decoder — not the link — is the bottleneck. The host then restores the pre-series rung once, ignores queue-only congestion and ladder step-ups for the rest of that session (slow sends still step down), and logs `Decoder-bound client detected`. Clients that never hit this pattern are unaffected. Resolution is deliberately not adapted (a mode change would re-plug the virtual display and disturb the desktop layout).
- Windows applies a tighter 30 Mbps cap after display-pixel scaling; other backends retain the shared 60 Mbps ceiling. While an Android client remains connected in background keep-alive, its visibility report lowers ffmpeg to below-normal process priority; when visible, priority returns to high. The default background behavior already stops video encoding, freeing its CPU/GPU work.
- If the host's read pump falls behind the encoder (12+ of 16 queued output chunks on 4 consecutive reads, e.g. under heavy host CPU load), the pipeline restarts at the same bitrate rung (at most once per 3 s) so the tablet gets a fresh keyframe instead of seconds-old video.
- When the first video session starts, the host process itself is also raised so host load from other applications cannot starve the pump that drains the encoder: Windows uses above-normal priority class, 1 ms timer resolution and an EcoQoS/power-throttling opt-out; Linux requests nice -5 (best effort, capped by `RLIMIT_NICE`).
- **Session health & dead-peer detection (M4)**: every video send carries a 5 s write deadline; the upstream stats reader doubles as a fast disconnect signal (EOF). Either path ends the session promptly, removing the virtual display. As a backstop against ghost displays (e.g. after suspend/resume), a watchdog checks connectivity every 30 s and ends the active video session — removing its virtual display — when no tablet is connected (`display_watchdog.go`). The same watchdog also removes every virtual display — including ones held by a finished session or left by another host process, which the session teardown alone does not reach (X11 vkms output, Mutter/wlroots monitors, Windows VDD monitors) — once no tablet has been connected for the advanced setting `idleDisplayTimeoutSec` (default 600 s, 0 disables). A summary line (`Video [encoder @ bitrate]: sent … ; client: recv/rend/decode/queue`) is logged every 5 s during streaming, preceded by `client skipped N stale frames since the last summary` when the client had to catch up after a stall.
- **Input injection**: every `input` packet is injected into the OS pointer stack, mapped from normalized tablet coordinates onto the active session's display rectangle (primary display when no video session is running).
  - **Windows** (10 1809+): synthetic pointer devices (`CreateSyntheticPointerDevice`/`InjectSyntheticPointerInput`) — a PT_PEN device with pressure (0–1024), per-axis tilt, hover, barrel button and eraser inversion, and a PT_TOUCH device carrying up to 10 simultaneous contacts (full contact frame re-injected per event).
  - **Linux**: a `/dev/uinput` virtual pen tablet ("DrawSplay Pen") advertising ABS X/Y (0–32767), pressure (0–4096), per-axis tilt (±90°), pen/eraser/finger tools and stylus buttons. On an extended display the host maps the device to it through the per-tablet "Map to Monitor" setting (keyed by the monitor's vendor/product/serial) of GNOME, under Xorg or Wayland, or Cinnamon; under other X11 window managers the injector aims at the output's rectangle within the X screen instead. Requires `/dev/uinput` write access (udev rule or sudo, see README).
  - **Palm rejection**: finger events are dropped while the pen was in range/contact within the last 500 ms.
  - **Per-type enable controls**: persisted `touchEnabled` and `penEnabled` settings independently gate finger events and stylus/eraser events before injection. Both default to enabled. Changing either from the web UI closes active input sessions so `cancelAll` immediately lifts held contacts; Android reconnects automatically. Tablet-attached mouse and keyboard input are unaffected.
  - **Single-tablet host mode**: the host accepts one concurrent input session (one tablet) per host process. Additional tablet/session connections are rejected until the active one disconnects.
  - **Per-device calibration**: host-side pen mapping supports per-tablet calibration (`scaleX`, `scaleY`, `offsetX`, `offsetY`) keyed by the tablet `hello.device` identity. Calibration is applied before OS injection and persisted to `calibration.json` in the user config directory.
  - If the injection mechanism is unavailable, the host logs the reason and continues capture-only (diagnostic logging still works).
- **Keyboard passthrough (M5)**: `key` packets from a physical keyboard attached to the tablet are injected into the host.
  - A shared Android-keycode table (`keymap.go`) maps to both Windows virtual-key codes and Linux evdev codes: letters, digits, F1–F12, modifiers (Shift/Ctrl/Alt/Meta, left/right), navigation, editing, punctuation, and the numpad. Mapped keys go through the OS key stack, so host shortcuts (Ctrl+C, Alt+Tab, …) work.
  - **Windows**: `SendInput` with virtual-key codes; unmapped keys that carry text fall back to per-character `KEYEVENTF_UNICODE` injection on key-down.
  - **Linux**: a second `/dev/uinput` device ("DrawSplay Keyboard") advertising every mapped evdev code; unmapped keys are dropped (uinput has no Unicode path).
  - **Security control**: persisted `keyboardEnabled` can disable all keyboard packet injection while keeping display and pen/touch input active.
- Accepts newline-delimited JSON packets from Android client on the input channel.
- Enables low-latency TCP behavior (`TCP_NODELAY`) on host connection.
- Enforces maximum packet size (`64 KiB`) and closes oversized packets.
- Requires an initial `hello` packet within `5s` and closes sessions that do not send it; the read deadline is cleared once after `hello` (no per-packet deadline syscalls).
- Limits concurrent input sessions (currently `8`) to avoid resource exhaustion.
- Per-session goroutines (input and video) run under panic recovery: a panic in one session is logged with a stack trace and drops only that session; the listeners keep accepting and the client reconnects. A second host launch detects the running instance (web UI port probe) and exits with a clear message instead of a raw bind error.
- Recognized input packet types:
  - `hello`
  - `input`
  - `key` (M5)
- Per-event diagnostics are logged through a bounded async channel (capacity `2048`); under console backlog, diagnostic lines are dropped and counted instead of ever blocking the network read path.
- **Settings web UI**: `http://127.0.0.1:27180` serves an embedded settings page plus a JSON API (`GET/PUT /api/settings`, `GET /api/status`, `GET /api/setup-check`, `POST /api/setup/remediate`, `GET /api/diagnostics`, `GET/PUT /api/calibration`, `POST /api/session/input-lock`, `POST /api/session/disconnect`, `POST /api/session/reconnect-video`, `POST /api/autostart`, `POST /api/stop`, and tablet display-preference clearing). State-changing calls require a per-process anti-CSRF token in `X-DrawSplay-CSRF`; browser requests also require a matching loopback origin/authority. `/api/status` exposes degraded-mode warnings (ADB, encoder/tooling, Linux uinput), active tablet identity, session input-lock state, first-run checklist status, latest client decode stats, and lightweight reconnect/restart counters. Advanced controls include strict preferred-ADB-serial trust, keyboard passthrough enable/disable, and automatic tool download/install control. On Windows+Android/tablet sessions, resolution/fps controls are hidden and Windows Display Settings controls those values; capture fps follows the active Windows display refresh, capped at the tablet panel's highest refresh rate. The status line shows the tablet's reported refresh rates. Executable settings accept only the default bare names or existing absolute local executable files; URLs, relative paths, UNC/mapped network locations, and empty values are rejected. Settings persist to the per-user config file (`%APPDATA%\DrawSplay\config.json` / `~/.config/DrawSplay/config.json`).
- **Login autostart ("service" mode)**: `-service install|uninstall|status` (also toggleable from the web UI). The host must live in the user's desktop session — session-0 Windows services cannot use DXGI duplication or synthetic input injection, and Linux needs the user D-Bus/desktop session — so autostart is per-user: an `HKCU\...\CurrentVersion\Run` entry on Windows launching `host -background` (which respawns itself console-less and logs to `%APPDATA%\DrawSplay\host.log`), and an XDG autostart entry (`~/.config/autostart/drawsplay-host.desktop`) on Linux. Installing it disables the legacy systemd user-unit startup link; an enabled legacy install is migrated automatically on status checks. Linux background logs go to `~/.config/DrawSplay/host.log`.
- **Windows tray by default**: on Windows `-background` defaults to true, so the host runs console-less with a tray icon (Open Settings / Start / Stop / Exit); release builds use the GUI subsystem so no console window ever appears. `-background=false` runs in the foreground and logs to the launching terminal. Launching it again while it is running opens the settings page instead of starting a second instance.
- **Windows install / uninstall**: `DrawSplay.exe -install` (the release asset `drawsplay_installer_<version>.exe` runs it when launched with no arguments, as does any executable whose name contains "installer"; it is also offered when an uninstalled `DrawSplay.exe` is launched with no arguments, and declining is remembered). Before the UAC prompt it asks about a desktop shortcut and sign-in autostart (`-autostart` / `-no-autostart` with `-quiet`; a quiet reinstall keeps the current autostart state). It runs one elevated phase that copies the host to `%ProgramFiles%\DrawSplay\DrawSplay.exe` plus `DrawSplay-Uninstall.exe`, downloads the verified ffmpeg runtime there, installs DrawSplay's bundled IddCx virtual-display driver (unless `-no-driver-install`), installs the signed WinUSB package for `18d1:2d00`-`2d05` (plus `&MI_00` of 2d01/2d04/2d05) and attached Android devices, registers `\DrawSplay\Bind Tablet USB Driver` (runs `DrawSplay.exe -winusb-bind-attached`), and adds an Apps & Features entry. The per-user phase writes (or removes) the HKCU Run autostart, Start Menu shortcuts (Roaming), an optional desktop shortcut (asked interactively; `-desktop-shortcut` for `-quiet`, and a quiet reinstall keeps an existing one; path recorded in `install.json`), and `%APPDATA%\DrawSplay\install.json`. While `install.json` exists the host never elevates on its own: the virtual-display driver install is skipped at launch (the host runs in mirror mode if the driver is somehow missing). `DrawSplay-Uninstall.exe` (or `-uninstall`; `-quiet`, `-keep-settings`) relaunches from `%TEMP%`, stops running hosts, removes the task, Apps & Features entry, install folder, autostart, Start Menu and desktop shortcuts after one UAC prompt, and deletes `%APPDATA%\DrawSplay` unless settings are kept. Dependencies are removed too: WinUSB driver packages generated by DrawSplay are always deleted (`pnputil /delete-driver /uninstall`) together with the DrawSplay driver-signing certificates; the DrawSplay IddCx virtual-display driver is removed (`pnputil /delete-driver`) when DrawSplay installed it (`vdd-installed-by-drawsplay` marker / `install.json` `vddOwned`); any legacy Parsec VDD driver a previous DrawSplay version installed is also cleaned up best-effort when the Parsec app is absent, otherwise only after confirmation (`-remove-shared` with `-quiet`), as are Zadig (libwdi) WinUSB packages for the AOA IDs. Other vendors' drivers (e.g. Samsung, SuperDisplay) are never touched.

### 2.2 Android app (`android/`)

- **Zero-touch connection model**: the app auto-connects (retrying every `1s`), reconnects automatically after drops, and has no manual connect/disconnect controls. A host policy packet can enable background persistence: when enabled, Android keeps the input channel and a headless video sink alive while the app is backgrounded, so the host monitor/session persists until the app is closed or the link is severed. A "Waiting for host…" overlay shows until video flows. Native display size and supported refresh modes use modern Activity/display APIs on Android 11+, with the deprecated default-display path retained only for Android 8–10 compatibility.
- Fullscreen immersive UI: hardware-decoded video surface with a transparent input overlay aligned 1:1, screen kept on, locked to the landscape aspect (`sensorLandscape`) but following the tablet's sensor between normal and reverse landscape, so the picture and touch mapping stay upright whichever way round the tablet is held/mounted.
- **Video**: receives framed Annex-B NAL units, configures MediaCodec for negotiated `codec` (`h264` or `hevc`) from in-band parameter sets, and decodes to the surface. Decoded frames are released with explicit presentation timestamps on an even cadence derived from the config packet's `fps`, so network/decode arrival jitter does not map onto screen time (video judder); the cadence stays at most one frame interval ahead of the clock and late frames resync and render immediately, keeping the pen-latency profile intact. The stream fps is also passed to `Surface.setFrameRate` (Android 11+; `CHANGE_FRAME_RATE_ALWAYS` on Android 12+) and the activity requests the panel's highest supported display mode at its current resolution (never a faster mode at a lower resolution). The window-level request prevents adaptive-refresh firmware from classifying the video surface as idle and silently dropping a 60 fps stream to 30 Hz; on a 120 Hz panel each 60 fps frame is presented for exactly two refreshes. Requests `KEY_LOW_LATENCY` on Android 11+ and sizes decoder input buffers to 4 MB (`KEY_MAX_INPUT_SIZE`) so large high-bitrate IDR frames are never truncated by a decoder's default buffer size (an oversized NAL that still doesn't fit is dropped with a log line rather than crashing the feed). **Decoder compatibility:** tablet SoCs differ in which optional MediaFormat keys and which decoders work (e.g. Unisoc `OMX.sprd.h264.decoder` fails `configure()` on `KEY_LOW_LATENCY`), so startup walks a fixed ladder (`DecoderConfigPlan`): the platform-default hardware decoder with the full format first (so capable devices are unaffected), then progressively fewer optional keys (`FULL` → `STANDARD` → `RATE_HINTS` → `BASIC` → `MINIMAL`), then the other hardware decoders the device lists, and only then a software decoder (`c2.android.*`/`OMX.google.*`). The first combination that starts is remembered and tried first on reconnect; the walk happens once at decoder start and adds nothing per frame. If a started hardware decoder keeps erroring at runtime (3 times across reconnects), software decoding is tried first; the count resets once a hardware decoder proves healthy (120 decoded frames). Mid-session parameter sets (adaptive-bitrate restarts on the host) are fed in-band to the running decoder. **Stall recovery:** a stall anywhere between the host's encoder and the decoder (Wi-Fi roam, USB/adb hiccup, busy host) leaves the frames captured meanwhile buffered on the way, and they arrive together when it clears. In latency mode (the host's default) a decoded frame is released without rendering when at least two more newer pictures than the decoder's usual pipeline depth are already queued behind it (`StaleFrameTracker`, matching outputs to queued pictures by input timestamp) **and** the decoder is catching up (its last four outputs came over 4/3 faster than the stream's frame interval), so the picture jumps to the newest frame instead of replaying the stall in fast forward; one frame is still shown every 100 ms of a long catch-up. A decoder that is merely slower than the stream is never thinned this way. Independently, when a keyframe (H.264 IDR, HEVC IDR/BLA; not CRA) arrives with three or more older pictures still waiting for decoder input, those pictures are dropped (keeping the latest parameter sets). Skipping turns itself off if the decoder doesn't preserve input timestamps. Reports per-interval decode statistics upstream on the video socket every 250 ms (see §3.0).
- Connects through Android's USB accessory API when AOA is attached, with a `3s` connect timeout per attempt; loopback TCP/ADB and Wi-Fi remain selectable fallback endpoints.
- Enables socket low-latency settings (`tcpNoDelay`, keepalive, tuned buffers); input writes are UTF-8 with one flush per batch.
- Captures touch/stylus events from input pad view, including:
  - Historical (intra-frame batched) move samples, so full stroke resolution is preserved (pens report 240 Hz+ on many tablets).
  - Pen hover events (`hover_enter`, `hover_move`, `hover_exit`).
  - Stylus button events (`button_press`, `button_release`) and the live `buttons` bitmask.
  - Pen `tilt` and `orientation` axes (radians).
- Requests unbuffered input dispatch per gesture, removing display-refresh input batching latency.
- Timestamps are derived from the input driver event time mapped to epoch ms (per historical sample), not from the delivery time.
- Sending is event-driven with no artificial pacing: batches are written immediately when the link is idle.
- Under transport backpressure only, contiguous move/hover batches are coalesced (newest kept) and the send queue is bounded (`32` batches, oldest move batches dropped first); `down`/`up`/`cancel`/button boundary events are never dropped.
- **Keyboard passthrough (M5)**: hardware key events are consumed by the activity and forwarded as `key` packets (with the Android keycode, meta state, and produced characters for the host's Unicode fallback). Device-local keys stay local: back, home, app-switch, power, volume.
- The desktop cursor is included in the captured video on Windows (`ddagrab:draw_mouse=1`) and by the Linux capture backends; there is no separate Android cursor overlay or cursor packet.

## 3. Protocol Reference (Current)

### 3.0 Channels

| Port  | Direction     | Framing                          | Content            |
|-------|---------------|----------------------------------|--------------------|
| 27183 | tablet ↔ host | **Mux-v1 binary frames** `[magic/version/stream/flags/seq/len/payload]` | Upstream `hello`/`input`/`key`, downstream `displayMode`/`clearDisplayMode`/`sessionPolicy` |
| 27184 | host → tablet | binary `[type:1][len:4][payload]` (big-endian) | video config + H.264 NALs |
| 27184 | tablet → host (same socket, upstream) | newline-delimited UTF-8 JSON | decode statistics (`stats`) |

Video channel packet types (downstream):

- `0` (config): JSON payload `{"codec":"h264|hevc","hostCodecs":["h264","hevc"],"encoder":"h264_nvenc","fps":60,"bitrate":"8000k"}`, sent at session start **and after every adaptive-bitrate restart**, always before the bitstream that follows. `codec` is the selected codec; `hostCodecs` is an additive capability scaffold. Clients must tolerate unknown fields and repeated config packets mid-session.
- `1` (nal): one H.264 Annex-B NAL unit, start code included. SPS/PPS are delivered in-band before pictures (and re-delivered after every pipeline restart).

Video channel upstream (`stats`, every 250 ms; all counters are per-interval deltas; further optional fields such as `avgPresentMs`, `frameDrops`, `refreshHz`, `renderMode` and `pacingGuardrail` are informational):

```json
{
  "type": "stats",
  "intervalMs": 250,
  "framesReceived": 15,
  "framesRendered": 15,
  "avgDecodeMs": 6.4,
  "queuedFrames": 1,
  "staleFrames": 0
}
```

- `framesReceived`: picture NALs (types 1–5) queued for decode.
- `framesRendered`: decoded frames released to the surface for display (stale frames skipped in latency mode are not counted).
- `avgDecodeMs`: mean in-codec latency (queue → output) over the interval, across all decoded frames.
- `queuedFrames`: decode backlog at report time — the host's congestion signal (≥ 8 triggers a bitrate step-down unless `staleFrames` > 0 in the same report).
- `staleFrames`: frames never shown because the client was catching up after a stall: decoded but not rendered, dropped ahead of a newer keyframe, or dropped by the 128-NAL backlog bound. Omitted by older clients.

Input packets on port 27183 are now carried inside mux frames (stream `control` for `hello`/host-control messages, stream `input` for `input`/`key`); payloads remain UTF-8 JSON.

### 3.0.1 USB accessory (AOA) transport (default, no ADB)

On by default (`aoaEnabled`, flag `-aoa-enabled`; settings UI "USB accessory transport (no ADB)"). It lets the tablet connect over USB **without Developer options or USB debugging**. The adb-reverse tunnel is now opt-in (`adbTunnelEnabled`, flag `-adb-tunnel`): when off, the host does not run adb, does not download it, and the web UI hides the adb settings and the ADB setup-checklist item (showing a "USB accessory link" item instead).

- `aoaAutoSwitch` (flag `-aoa-auto-switch`, default on) controls automatic switching; when off the host only switches when the web UI button (`POST /api/tablet/switch-aoa`) is pressed. That request also overrides the Linux File-transfer protection below.
- On Linux, a device that exposes an MTP/PTP interface (user selected File transfer) is not switched to accessory mode, so file transfer keeps working; select "No data transfer" to resume the accessory link.
- The host finds Android devices (known vendor IDs; extra vendor IDs via `DRAWSPLAY_AOA_VIDS`, comma-separated hex) and sends the AOA control requests (get-protocol, identification strings `DrawSplay`/`Tablet`, start). The tablet re-enumerates as `18d1:2d00`-`2d05`; when `autoLaunchAndroidApp` is on, Android can launch the app (accessory intent filter, `res/xml/accessory_filter.xml`) after the user accepts the one-time USB permission prompt.
- On Windows, WinUSB must be bound to both the tablet's normal USB identity (to send the switch requests) and its AOA accessory identity (to carry data). The host installs its own signed WinUSB package for both (no Zadig); see `packaging/windows/README.md`. The Windows setup checklist reports when the normal-mode identity has no WinUSB interface.
- **Switching a device to accessory mode ends its adb session**, so a tablet cannot be used over adb at the same time.
- The accessory bulk pipe carries one byte stream of link frames: `[type:1][channelId:2][len:2]` (big-endian) plus payload, payload ≤ 16000 bytes. Types: `1` HELLO (channel 0, payload `"DSAOA"` + version byte `1`), `2` OPEN (tablet → host, payload = kind: `1` input, `2` video), `3` DATA, `4` CLOSE.
- The tablet sends HELLO first (and repeats it every 3 s so a restarted host can handshake); the host answers once. A second HELLO from the host resets all virtual connections.
- Each OPEN becomes a virtual connection handed to the same handlers as the TCP listeners, with transport name `usb` (same session ownership as adb). Input uses the mux-v1 framing above; video uses the video protocol above.
- Writes that are an exact multiple of the endpoint packet size are followed by a zero-length packet.
- Linux: pure-Go usbfs backend; the Linux install script installs `packaging/linux/70-drawsplay-aoa.rules` (udev `uaccess`) automatically. Windows: WinUSB must be bound to the tablet and the accessory device; `-install` installs a WinUSB package covering the accessory IDs and attached tablets. The WinUSB package is signed on the PC itself (as Zadig/libwdi does): setup generates the INF and catalog, creates a self-signed code-signing certificate unique to the PC (`CN=DrawSplay USB Driver Signing (<PC name>)`), signs the catalog, trusts the certificate in LocalMachine `Root` and `TrustedPublisher`, and deletes its private key immediately, so nothing else can ever be signed with it. When an attached identity has no WinUSB interface, an installed host runs the `\DrawSplay\Bind Tablet USB Driver` task (no prompt; once per identity per run; result in `%APPDATA%\DrawSplay\winusb-bind-result.json`), which regenerates and re-signs the package with that identity added and removes the previous package and certificate. A portable host elevates `-winusb-bind-attached` via UAC at most once per identity ever (`winusb-attempts.json`). If binding still fails, the setup checklist says to reconnect or rerun the installer. The Windows USB backend is compile-tested; protocol and link behavior are unit-tested against fakes.
- There is no app-level authentication beyond the OS USB permission prompt and physical access.

### 3.1 `hello`

```json
{
  "type": "hello",
  "device": "Manufacturer Model",
  "android": "14",
  "app": "version-name",
  "screenWidth": 2560,
  "screenHeight": 1600,
  "screenRefreshRates": [60, 120],
  "prefWidth": 0,
  "prefHeight": 0,
  "prefFps": 0,
  "videoCodecs": ["h264"],
  "timestamp": 1720000000000
}
```

`prefWidth`/`prefHeight`/`prefFps` are the mode the tablet remembered (0 = none). For Android clients the host creates the session display at that mode, or on first connect at the tablet's native resolution and the panel's highest refresh rate at that size (60 fps if it reports none; Linux hosts cap at 60 fps) (the host's width/height/fps settings apply only to clients that report no native size). When the mode changes mid-session (OS display settings or web UI) the host pushes `{"type":"displayMode","width":W,"height":H,"fps":F}` (`fps` omitted when unchanged) and the tablet stores it. On Windows, a session display replaced by virtual-display self-recovery or by the mirror fallback starts a new baseline instead of counting as a mode change, and a mirrored display never pushes one (its mode is the host monitor's, not the tablet's). A new video session waits up to 3 s for the hello before creating the display.

Relaunch handling: the tablet's AOA link hello carries a random per-app-instance nonce after the fixed header. If the host sees a hello with a different nonce on a live link, the app was relaunched (install, crash, force-stop): the host closes the old virtual connections and answers with a new hello, so the app reconnects without unplugging or restarting the host. Hellos with the same nonce are keepalives; apps without a nonce behave as before.

Web UI Save: the settings page resends the host's current width/height unless the user edited the resolution controls, so a resolution picked in the OS display settings is never overwritten by a stale page.

Recommended resolution: the Android client may add `recommendedWidth`, `recommendedHeight` and `recommendedReason` to hello. It derives them from the tablet's RAM tier (≈1.1 MP budget at ≤3.5 GB, ≈2.1 MP at ≤4.5 GB, uncapped above) and the hardware H.264 decoder's 60 fps size limit, rounded down to the largest standard resolution that fits (e.g. 1320x824 → 1280x800). Omitted when the native size is fine. The host only displays it in the web UI (never enforced), and clients that omit it behave as before.

Per-host preferences: newer tablets send `"hostPrefs":true` in hello instead of pref values. The host replies `{"type":"hostInfo","hostId":"<32 hex>","hostName":"..."}` (hostId is persisted in the host config dir as `host_id`), and the tablet answers on the control stream with `{"type":"modePrefs","prefWidth":W,"prefHeight":H,"prefFps":F}` for that host (zeros if none). The tablet stores prefs under `host.<hostId>.width|height|fps`, so multiple hosts keep independent choices. Session display creation waits (up to 3 s) for `modePrefs`. Tablets without `hostPrefs` fall back to the prefs in hello.

`screenRefreshRates` (optional, additive) lists the refresh rates in Hz, rounded, ascending, that the tablet panel supports at its native resolution. The host keeps values in 24–240 and uses the highest as the panel rate: on Windows the virtual monitor is created at that rate, so it defaults to the tablet's refresh (for example 120 Hz), and capture fps is capped at it. Clients that omit it default to 120 Hz.

`videoCodecs` is optional for backward compatibility; its absence means a legacy H.264 client. Current negotiation selects the best common codec from the host/client advertised lists (today: `h264`, and `hevc` when both sides support it on the active host path).

### 3.2 `input`

`sessionPolicy` (host → tablet) carries the additive boolean `penPrediction` (web UI "Pen motion prediction", default on). When true the tablet appends one extrapolated `hover_move` sample (≈12 ms look-ahead, capped at 48 px, suppressed when slow or turning) to stylus hover batches; contact strokes are never predicted. Changing the setting restarts active input sessions so the new policy is pushed. Tablet-side the input channel serializes samples on a display-priority IO thread, writes each batch with a single socket write, keeps only the latest queued `hover_move`, and merges queued contact `move` samples (bounded) instead of dropping them.


```json
{
  "t": "input",
  "a": "down|move|up|pointer_down|pointer_up|cancel|hover_enter|hover_move|hover_exit|button_press|button_release|other",
  "x": 123.45,
  "y": 456.78,
  "p": 0.42,
  "ti": 0.35,
  "o": -1.2,
  "b": 0,
  "tt": "finger|stylus|mouse|eraser|unknown",
  "id": 0,
  "pc": 1,
  "w": 1920,
  "h": 1200,
  "ts": 1720000000000
}
```

Field notes:

- `tilt`: pen tilt from the surface normal, radians (`0` = perpendicular). `0` for tools without tilt.
- `orientation`: tool orientation, radians, as reported by Android (`getOrientation`).
- `buttons`: Android `MotionEvent` button-state bitmask (e.g., stylus primary/secondary buttons); `0` when none pressed.
- `timestamp`: epoch ms derived from the input driver event time (per historical sample), not the app delivery time.
- All three fields are additive; packets without them decode as zero values on the host.
- For backward compatibility the host accepts both compact keys above and the legacy long keys (`type`, `action`, `pressure`, `toolType`, `pointerId`, ...).

### 3.3 `key` (M5, tablet → host)

```json
{
  "t": "key",
  "a": "down|up",
  "k": 29,
  "m": 4096,
  "c": "a",
  "ts": 1720000000000
}
```

- `keycode`: Android `KeyEvent` keycode. Mapped through `keymap.go` to VK / evdev codes.
- `meta`: Android meta-state bitmask (diagnostic; modifiers are tracked by the host OS from the forwarded modifier keys themselves).
- `chars`: character(s) this key produces with the current meta state; used only as the Windows Unicode fallback for unmapped keycodes.
- Host decoding remains backward-compatible with the legacy key names (`type`, `action`, `keycode`, `meta`, `chars`, `timestamp`).

### 3.4 Notes

- In the current low-latency profile, no per-event `ack` packet is required.
- Move packets may arrive at the full digitizer sample rate: each Android `MotionEvent` is expanded into its historical samples (oldest first), each as its own `input` packet.
- Cross-device latency instrumentation is not yet finalized in code.

### 3.5 Native media helper and video-channel extensions (additive)

* Optional `drawsplay-media` helper (`native/drawsplay-media/`, Linux X11 + VA-API). It is one extra pipeline candidate in front of the ffmpeg candidates; a missing binary, failed `--probe`, unsupported driver or invalid first frame falls back to ffmpeg. Setting `nativeMediaHelper`: `auto` (default; Intel iHD only), `on` (any VA driver that passes the test encode, e.g. AMD), `off`. NVIDIA on Linux keeps ffmpeg NVENC. Windows is unchanged (ffmpeg).
* Helper capture is damage-driven (no fixed tick); config packet gains `captureMode:"damage"`, and the tablet presents frames immediately.
* Hello gains `videoFeatures` (`tile1`, `meta1`); input packets gain `sq` (sequence). Host only sends the new video packets to clients that advertised them:
  * type 2 TILE (lossless zlib sparse tiles), type 3 MODE (0 video, 1 tile), type 4 FRAME_META (`u32 inputSeq`, `u32 pipeUs`, big-endian).
  * Stats gain `echoMs` (input send to the frame carrying it); logged in the session summary.
* Helper wire format: `native/drawsplay-media/PROTOCOL.md`.

### 3.6 Wi-Fi major-2 envelope component (not yet a product session)

The independent Go/Kotlin Wi-Fi codec uses a fixed 36-byte big-endian
header: `type:u8, flags:u8, headerLen:u16, payloadLen:u32, epoch:u64,
sequence:u32, captureMonoUs:u64, durationUs:u32, reserved:u32`.
Types are 1 hello, 2 control, 3 input, 4 attach, 5 config, 6 video AU.
Flags/reserved are zero, headerLen is 36. Bootstrap hello alone uses zero
epoch/sequence; all session records require nonzero values. Payload maxima
are 4 KiB for hello/control/input/attach, 64 KiB for config and 1 MiB per AU.
Duration is zero except for AU records. Unknown/oversized/malformed values
fail before payload reading. A separate session sequence guard rejects
replay, wrong epochs and uint32 wrap.

Header-first reads permit validation before leasing caller-owned payload
storage; codecs reuse header storage and never allocate payload buffers.
The Kotlin header object is borrowed until the next read. This code is not
yet admitted by normal app/host sessions and does not change USB framing,
major versions or current packet meanings. Pairing, authenticated major-2
negotiation and channel association still need implementation. See
`docs/wifi-wire.md` for layout, ownership and actual Tab A8 interoperability
results.

### 3.7 Wi-Fi enrollment, mutual TLS and trust components (not integrated)

The Wi-Fi-only SRP-6a enrollment components bind an eight-digit code, both
SPKI identities, protocol version and a fresh transcript. Invitations are
local-only, 120 seconds, five attempts and single-active-exchange. Tablet
proof precedes host proof; failed or replayed confirmation grants no trust.
The Go/Kotlin implementations passed a real cross-language offline exchange.

Independent paired-session TLS factories require mutual P-256 identities,
SPKI pinning and TLS 1.2 ECDHE-ECDSA/AES-GCM or TLS 1.3. The new Android
factory bounds total handshake time even for slow-dribble peers.
Host trust persists under private Linux permissions or Windows user
DPAPI/ACLs; Android uses non-exportable Keystore keys and bounded
AES-GCM-authenticated records with no plaintext fallback. An isolated
Tab A8 test verified Android trust persistence and tamper rejection.

The opt-in `cmd/wifi-pair` preview now discovers a selected tablet, performs
numeric PAKE and pinned mTLS on the same host-initiated connection, then
requires local approval and confirms durable trust. An isolated API-34
Tab A8 test exercised both stores with actual Keystore-backed TLS.
The pre-TLS enrollment format starts `DSEP, major:u16=2, reserved:u16=0`
and binds the actual host/tablet SPKI hashes to the PAKE transcript; it is
separate from both USB and the post-authentication major-2 envelope.

These components do not yet expose enrollment in the normal tablet app or
authorize product video/input sessions. Live revocation/disconnect,
automatic reconnect and production lifecycle wiring remain required.
Authenticated capability/channel negotiation is described below.
The isolated probe requires API 33; Windows strict-LAN dialing
and older-Android availability fallbacks remain outstanding.
Existing USB/scaffold trust behavior is unchanged. Full details
and limitations: `docs/wifi-pairing.md`.

### 3.8 Paired Wi-Fi channel negotiation (isolated development preview)

These messages use the Wi-Fi major-2 binary envelope, not USB JSON or
AOA framing. After saved-trust mutual TLS, the tablet sends a Hello and the
host returns negotiated limits in its Hello. The host initiates TCP but
remains TLS server on both independent control and video sockets.
Hello payloads are exactly 96 bytes, unsigned big-endian:

| Offset | Field |
| --- | --- |
| 0, 2 | Minimum/maximum protocol major, u16 each; range must include 2 |
| 4, 5 | Role u8 (tablet 1, host 2); reserved u8 = 0 |
| 6 | Codec mask u16; bit 0 = H.264 baseline |
| 8, 10, 12 | Maximum width, height, FPS, u16 each |
| 14 | H.264 level u16: 31, 32, 40, 41, 42, 50, 51 or 52 |
| 16, 20 | Maximum access-unit bytes and compressed-queue bytes, u32 each |
| 24, 26 | Queue slots u16; statistics schema u16 = 1 |
| 28 | Feature mask u32; bit 0 = low latency |
| 32, 64 | Actual authenticated SPKI SHA-256 and fresh nonce, 32 bytes each |

Dimensions are 320..8192, FPS 1..120, access units 64 KiB..1 MiB,
slots 2..4, queue at most 8 MiB and at least access-unit maximum times
slots, checked without integer overflow. Pins/nonces must be nonzero;
the role and pin must match the authenticated peer. Negotiation takes
minimum limits and known-feature intersection. Independent maxima are
**not a selected display mode**: the capture owner must still validate
encoder/decoder support and H.264 macroblock limits before starting media.

The host then sends control sequence 1 with a fresh nonzero epoch and
56-byte payload: subtype u8 = 1, reserved u8 = 0, schema u16 = 1,
session ID[16], secret ticket[32], lifetime-ms u32 = 5000.
One control session is active per coordinator; a bounded ring remembers
64 recent session ID/epoch generations. IDs, epochs and tickets use
cryptographic randomness. Tickets are never logged or persisted.

The separately authenticated video socket must present an attach envelope
with that epoch, sequence 1 and 52-byte payload: subtype u8 = 2,
reserved u8 = 0, schema u16 = 1, session ID[16], ticket[32].
The host checks the same tablet identity and matching ID/epoch/ticket.
The ticket expires exactly five seconds after host session creation,
is single-use even on a failed attachment, and is cleared on close.
Successful attachment receives video control sequence 1, same epoch,
payload `[3, 0, 0, 1]`. Subsequent control and video sequences each
start at 2 in independent sequence spaces.

Setup has an absolute five-second deadline and cancellation closes blocked
I/O. A failed video setup invalidates the binding; its owner closes control
too. Closing the Android control owner closes its associated video socket.
These are admission primitives, **not running capture/decoder/input pumps**.
`wifi-pair -verify-session` and the isolated API-33+ probe exercise fresh
saved-trust channels without media. API-34 Tab A8 evidence is recorded in
`docs/performance/tab-a8-wifi-session-2026-10-22.json`; automatic reconnect,
normal-app availability and performance acceptance are still outstanding.

**Synthetic Wi-Fi media (isolated test preview only).** The Android Wi-Fi
receiver (`video/WiFi*.kt`) validates a bounded baseline-AVC stream before
any MediaCodec use; this is not wired into the product display path. The
config SPS must be progressive constrained baseline within the negotiated
level and maxima; the coded size may exceed the visible size only by
macroblock alignment padding (crop under 16 px) and must stay within the
maxima rounded up to 16; `max_num_ref_frames` must fit the level's decoded
picture buffer (MaxDpbMbs / coded macroblocks, capped at 16). Each access
unit is one AUD-delimited picture: all its slices must share PPS, IDR flag,
`nal_ref_idc` zero/non-zero status, `frame_num`, `idr_pic_id` and (POC type
0) `pic_order_cnt_lsb`, with strictly increasing `first_mb_in_slice`;
otherwise the stream is rejected. On end-of-stream the decoder drains only
after any held post-flush recovery IDR has been submitted.
## 4. Target MVP Functionality

1. Extend desktop onto a virtual display represented by the Android tablet. **(done — M2)**
2. Stream host-rendered frames to tablet over USB with low latency. **(done — M1)**
3. Return touch/stylus events to host and inject them into OS input stack. **(done — M3)**
4. Recover gracefully from cable disconnect/reconnect. **(done — M4: client auto-reconnect + host write deadlines/upstream-EOF dead-peer detection + adaptive bitrate)**
5. Provide per-device calibration and orientation mapping.

## 5. Planned Major Components

### 5.1 Host

- Session management and device lifecycle.
- Video capture + encode pipeline (prefer HW encode path). **(done)**
- OS virtual display integration: **(done)**
  - Windows: DrawSplay's own bundled IddCx indirect display driver.
  - Linux: Mutter ScreenCast virtual monitor (GNOME Wayland); KWin virtual output (KDE Plasma 6); headless outputs with wlr-screencopy (sway, Hyprland); virtual RandR output (X11, `vkms`); x11grab mirror fallback.
- Input injection bridge (touch/pen). **(done)**

### 5.1.1 Known verification limitations

- Linux paths (Mutter D-Bus, GStreamer pipelines, uinput injection) compile (`GOOS=linux go vet` clean) but were authored on a Windows machine and need runtime verification on Ubuntu GNOME Wayland. The GNOME virtual monitor's arrangement logic follows Mutter's source (46 and 50: configuration verification, the linear fallback layout, the stored/previous configuration lookup) and was checked against a headless GNOME Shell 50 and a simulated DisplayConfig in unit tests; recording the monitor's PipeWire node with GStreamer and the Settings interactions still need a live session. The KDE Plasma, sway and Hyprland paths follow those compositors' sources and protocols and are unit-tested against fake compositors (i3-ipc, Hyprland sockets, Wayland wire protocol); they still need live verification.
- Windows extend mode is verified live with DrawSplay's own bundled IddCx driver (Surface Pro 8 / Intel Iris Xe driving a Samsung tablet at its native 2960×1848, 120 Hz). Live `ddagrab` capture and visible pointer movement cannot be verified while the desktop session is locked (DXGI duplication returns "access denied" on locked desktops); injection calls themselves succeed.

### 5.2 Android

- Hardware decode/render pipeline (e.g., `MediaCodec` + Surface).
- Input capture subsystem (multi-touch + stylus metadata).
- Optional calibration and diagnostics UI.

## 6. Non-Goals

- Wi-Fi remains optional and disabled by default; USB is the primary transport path.
- Manual connect/disconnect UI inside the tablet app remains out of scope — the app-side connection model is zero-touch by design.
- Multi-tablet support (post-MVP).
- Advanced gesture mapping and shortcut layers (post-MVP).
- Cloud or account features.

## 7. Functional Change Policy

When behavior changes:

1. Update this file in the same change.
2. Keep protocol examples in sync with code.
3. Document backward compatibility expectations for packet changes.
