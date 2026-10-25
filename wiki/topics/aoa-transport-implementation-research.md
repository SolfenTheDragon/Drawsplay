---
title: AOA Transport Implementation Research (Linux and Windows)
type: topic
tags: [transport, usb, aoa, linux, windows, research]
created: 2026-10-15
updated: 2026-10-15
sources:
  - wiki/topics/superdisplay-comparison-and-adb-free-transport.md
summary: Web research on implementing an Android Open Accessory host transport in the Go host and Kotlin app on Linux and Windows.
---

# AOA (Android Open Accessory) Host-Side Transport for DrawSplay — Final Research Report

Legend: **[V]** verified against a primary source (cited), **[S]** secondary-sourced/corroborated but not independently re-verified, **[I]** inference/estimate, **[GAP]** searched for but not found.

---

## 1. AOA Protocol Spec Details

### 1.1 Control requests — **[V]** [source.android.com/docs/core/interaction/accessories/aoa](https://source.android.com/docs/core/interaction/accessories/aoa)

| Request | bRequest | bRequestType | wValue | wIndex | Data |
|---|---|---|---|---|---|
| **GET_PROTOCOL** | 51 (0x33) | `USB_DIR_IN \| USB_TYPE_VENDOR` | 0 | 0 | 16-bit LE protocol version returned by device |
| **SEND_STRING** | 52 (0x34) | `USB_DIR_OUT \| USB_TYPE_VENDOR` | 0 | string ID (0–5) | zero-terminated UTF‑8 string, max 256 bytes |
| **START** | 53 (0x35) | `USB_DIR_OUT \| USB_TYPE_VENDOR` | 0 | 0 | none |

String IDs **[V]**: 0=manufacturer, 1=model, 2=description, 3=version, 4=URI, 5=serial.

**Critical gotcha [V]**, quoted directly from the spec: *"A USB accessory must send a header upon initial handshake… Though version is an optional field, if an Android app is installed that only matches on version, but the accessory doesn't send a version, Android devices running on Android 10 and below will reboot due to an exception being thrown in the system process."* → **never declare `version` in `accessory_filter.xml`**.

### 1.2 Re-enumeration PIDs

VID = **0x18D1** (Google) **[V]**. Quoted directly: *"`0x2D00` is reserved for Android-powered devices that support accessory mode. `0x2D01` is reserved for devices that support accessory mode as well as ADB."* `0x2D00` = one interface, two bulk endpoints; `0x2D01` = two interfaces (accessory + ADB), two bulk endpoints each; after enumeration, issue `SET_CONFIGURATION(1)` then locate the first bulk IN/OUT pair **[V]**.

The rest of the PID table is **[S]** (consistent across multiple ADK-header mirrors, not re-verified against raw AOSP header in this pass):

| PID | Accessory | Audio | ADB |
|---|---|---|---|
| 0x2D00 | ✓ | | |
| 0x2D01 | ✓ | | ✓ |
| 0x2D02 | | ✓ | |
| 0x2D03 | | ✓ | ✓ |
| 0x2D04 | ✓ | ✓ | |
| 0x2D05 | ✓ | ✓ | ✓ |

### 1.3 AOA v2 (audio/HID) — **[V]** not needed for DrawSplay

AOA v2 adds `SET_AUDIO_MODE` (request 58, must precede START) and four HID control requests (54–57: `REGISTER_HID`, `UNREGISTER_HID`, `SET_HID_REPORT_DESC`, `SEND_HID_EVENT`), all on EP0, no extra interface. Spec explicitly: *"Simply use the new features along with the original features."* Confirmed independently by scrcpy's `aoa_hid.c`, which implements only the HID requests, entirely separate from any bulk data path. **DrawSplay can ignore both and do only GET_PROTOCOL → SEND_STRING×N → START**, identical to AOA 1.0.

### 1.4 Timing/retry behavior — **[GAP confirmed]** + **[S]/[I]** practical guidance

Google's spec gives **no numeric timing guidance whatsoever** — only "wait for the connected USB device to re-introduce itself on the bus in accessory mode, then re-enumerate." This is a genuine documentation gap, confirmed by directly reading both AOA spec pages in full.

- `pyd-07/NetcoN-OpenTether` (`relay/aoa_linux.go`) **[V, fetched directly]** uses a 500ms wait before re-opening at the new PID, with backoff/retry — a real-world precedent, not an authoritative constant.
- [`daynix/UsbDk` issue #34](https://github.com/daynix/UsbDk/issues/34) **[V, direct quotes]**: maintainer confirms forcing a port-cycle/reset on the pre-AOA handle after sending START can desynchronize re-enumeration and strand the port. **Rule: after sending START, stop touching the old handle; just poll for the new VID/PID.**
- No shipped, maintained open-source AOA *host* (desktop) implementation documents an authoritative poll interval. **[I] Recommendation**: poll every ~100–250ms for several seconds, generous and tolerant rather than device-tuned.

### 1.5 OEM quirks — **[V] strong finding, but it's a Windows driver-layer quirk, not phone firmware**

The best-documented "OEM quirk" is **Windows-side**: [scrcpy issue #3654](https://github.com/Genymobile/scrcpy/issues/3654) **[V]**. Contributor yume-chan, direct quote: *"Because Windows USB API is bad, programs have to use an allowlist to determine which device is accessible. Usually, they only include the Windows built-in composite device driver (usbccgp), so they ignore devices with Samsung's custom driver."* Multiple users confirm uninstalling Samsung's official Android driver package (or re-binding with Zadig) fixes AOA/OTG access. **Samsung is specifically named as a repeat offender** for shipping a custom composite driver that pre-empts WinUSB binding.

Xiaomi/Huawei/MIUI-specific firmware-level AOA bugs: **[GAP]** — no citable primary source found (XDA, issue trackers) despite targeted searching. One lead, [Stack Overflow #79036242](https://stackoverflow.com/questions/79036242/) on AOA reconnect failures, could not be fetched (403) — flagged as unconfirmed, not as proof of absence.

### 1.6 MTP/PTP/charge-only mode — **[V]**

Spec: *"AOA does not currently support simultaneous AOA and MTP connections. To switch from AOA to MTP, the accessory must first disconnect… and then reconnect using MTP."* By protocol design, **you do not need to manually switch the phone out of MTP mode first** — GET_PROTOCOL *is* the probe, sent to whatever VID/PID the device currently presents (MTP, PTP, charge-only, or already-accessory). If it answers, proceed with SEND_STRING×N + START; the device then fully disconnects/re-enumerates on its own — that *is* the mode switch. No separate device-ID string probe exists, and no host-side MTP driver conflict needs resolving on Linux (old descriptor set simply becomes irrelevant). Caveat: on Windows a cached driver may try to auto-bind to the new node, contributing to the Zadig-related friction (§5).

### 1.7 Devices slow to re-enumerate — **[GAP]**. No authoritative list found; architect for a generous, tolerant polling window and catalogue problem devices empirically during QA.

---

## 2. Android/Kotlin Accessory-Side Implementation

All **[V]** from [developer.android.com/develop/connectivity/usb/accessory](https://developer.android.com/develop/connectivity/usb/accessory) unless noted.

**Manifest**: `<uses-feature android:name="android.hardware.usb.accessory"/>`, an `<intent-filter>` for `android.hardware.usb.action.USB_ACCESSORY_ATTACHED`, paired `<meta-data>` pointing to an `accessory_filter.xml` resource. (Legacy `<uses-library com.android.future.usb.accessory>` only applies to API<12 and is irrelevant today.)

**`accessory_filter.xml`**: `<usb-accessory manufacturer="…" model="…" version="…"/>`. Quoted directly: *"Filtering on version is not recommended… causes a NullPointerException on earlier versions of Android. This issue is fixed in Android 12."* **Recommendation: filter on manufacturer+model only, never version** — doubly motivated by this NPE and the Android ≤10 reboot bug in §1.1. Omitted attributes act as wildcards — **[I]**, standard/expected Android matcher behavior, not independently re-confirmed against AOSP matcher source this session, though consistent with the documented optionality of `version`.

**Core motivating claim, confirmed verbatim [V]**: *"**USB debugging isn't required for connecting accessories** but ADB might be necessary during development."* Independently corroborated by scrcpy's `doc/otg.md`: *"[OTG mode] allows to control the device … without using adb at all (so USB debugging is not necessary)."* **AOA genuinely does not require Developer Options/USB debugging.** (The Windows driver-binding friction in §5 is an OS/driver issue, not an Android-side requirement.)

**Permission flow [V]**: `UsbManager.hasPermission()` / `requestPermission(accessory, PendingIntent)` triggers the system dialog; *"If your application uses an intent filter to discover accessories as they're connected, it automatically receives permission if the user allows your application to handle the intent."* The well-known persistent "Use by default" checkbox is standard Android UX **[S]** — not re-quoted from the fetched doc text this session; its persistence keys only on manufacturer+model+version strings with zero serial/crypto binding, per direct AOSP source inspection (§7) — a genuine security-relevant finding.

**I/O [V]**, exact code from docs:
```kotlin
fileDescriptor = usbManager.openAccessory(accessory)
fileDescriptor?.fileDescriptor?.also { fd ->
    inputStream = FileInputStream(fd)
    outputStream = FileOutputStream(fd)
}
```
Run on a dedicated thread, not the UI thread.

**Max transfer size [V]** — confirms the 16384-byte figure directly: *"The Android accessory protocol supports packet buffers up to 16384 bytes… At a lower level, the packets are 64 bytes for USB full-speed accessories and 512 bytes for USB high-speed accessories. The Android accessory protocol bundles the packets together… into one logical packet."* Also baked into the **kernel** (`BULK_BUFFER_SIZE 16384` in `f_accessory.c` **[V]**, §6) — it's the device-side kernel per-request buffer size, with only `TX_REQ_MAX=4`/`RX_REQ_MAX=2` requests queued. **DrawSplay must implement its own length-prefixed framing** to reassemble H.264 frames/NALs larger than 16KB across multiple calls — Android does not do this for you.

**Detach handling [V]**: `ACTION_USB_ACCESSORY_DETACHED` should use a **dynamically-registered** `BroadcastReceiver`, explicitly because *"detached events are only sent to the application that is currently running"* — asymmetric with `ATTACHED`, which can be manifest-static for cold launch.

**Re-attach while already running — [I]**: not directly addressed in fetched docs; standard Android intent-dispatch semantics imply `ATTACHED` still fires on each physical reconnection regardless of in-memory app state — you must de-duplicate in your own code.

**Foreground service, Android 10–15 [V/S]**: The attach-triggered Activity launch itself needs **no** foreground service (privileged system-initiated launch, not a background service start, so not subject to Android 12+ `ForegroundServiceStartNotAllowedException` rules). A foreground service **is** needed to survive backgrounding. Android 14+ requires a declared `foregroundServiceType` (`connectedDevice` is the semantically right type) or the system throws `MissingForegroundServiceTypeException` **[V, general FGS rule]**; the exact `connectedDevice`-specific permission name/notification wording could not be directly re-fetched this session (client-tabbed docs page) — **[S]**, needs direct verification before implementation. OEM battery managers (Samsung/MIUI/Huawei) killing foreground services despite correct declaration is well-known community knowledge (`dontkillmyapp.com`) but **[S]**, not independently re-cited this session.

---

## 3. Host Go Libraries

| Library | Activity (verified via GitHub API) | Windows | CGO | Bulk/async | AOA precedent |
|---|---|---|---|---|---|
| **google/gousb** | 934★, last push 2024‑09‑13, not archived | Yes, manual libusb+pkg-config setup required; no pure cross-compile story | Yes (cgo+libusb-1.0) | Yes — `Endpoint.NewStream(bufSize, numTransfers)` wraps libusb's async queue | **Yes**, see below |
| **kevmo314/go-usb** | 21★, created 2025‑09, active | **No** — README states Linux/macOS only | **No** (pure Go via usbfs directly / IOKit on macOS) | Yes, explicit sync+async submit/reap API | None found |
| **purego+libusb** (`maitredede/puregolibs`, `krnelpan1c/OpenTether`) | 0–1★, experimental | Depends on purego's libusb support generally | No cgo, but still needs system `libusb.so/.dll` at runtime | Full libusb API via `ebitengine/purego` dlopen | One sibling project (`krnelpan1c/OpenTether`) targets AOA-adjacent Android tethering |
| karalabe/hid, sstallion/go-hid | 311★/97★, active | Yes | Yes (cgo+hidapi) | HID interrupt only | **N/A** — cannot address AOA's vendor-class bulk interface |

**[V]** gousb's own repo contains **no AOA/accessory example** — tree inspection shows only `example_test.go` and an `lsusb` example.

**[V] Positive finding**: a concrete real Go+gousb AOA implementation exists: [`pyd-07/NetcoN-OpenTether`, `relay/aoa_linux.go`](https://github.com/pyd-07/NetcoN-OpenTether) (small, 2★, but directly on-point). Defines the exact constants needed:
```go
aoaGetProtocol uint8 = 51
aoaSendString  uint8 = 52
aoaStartMode   uint8 = 53
googleVID gousb.ID = 0x18D1
aoaPID    gousb.ID = 0x2D00
aoaADBPID gousb.ID = 0x2D01
```
and corroborates probing OEM pre-enumeration VIDs (Samsung `04E8`, Xiaomi `2717`, Huawei `12D1`, HTC `0BB4`, Sony `054C`, OnePlus `2A70`, OPPO `22D9`, ZTE `19D2`) before/while negotiating the switch; uses `io.Pipe` to adapt blocking reads/writes into a `net.Conn`-like interface — directly comparable to DrawSplay's existing `host/usb_link.go` pattern.

**Verdict**: **gousb is the pragmatic default** — mature, async stream API matching AOA's queue-depth needs, at least one real AOA precedent — but its cgo dependency means Windows/cross-compile builds need a correctly staged libusb binary, not a `CGO_ENABLED=0` pure-Go build. `kevmo314/go-usb` is an attractive pure-Go story for Linux-only builds but too new/low-review and **has no Windows support**, unsuitable as the sole cross-platform backend today. The purego approach is a worthwhile middle ground (no C toolchain for cross-compiling the Go binary, still needs a bundled libusb shared library) worth prototyping if cgo cross-compilation becomes painful in CI.

---

## 4. Linux Host Setup

**udev [V]**: [`M0Rf30/android-udev-rules`](https://github.com/M0Rf30/android-udev-rules) (1,355★, pushed 2026‑10‑12) confirms the premise directly — its `51-android.rules` is a long per-OEM `GOTO`/`LABEL` dispatch table keyed on dozens of distinct `idVendor` values (Acer, Allwinner, Amazon, ASUS, Lenovo, LG, Motorola, etc. directly observed), because devices present their native OEM VID before AOA re-enumeration. Samsung/Huawei/Xiaomi-specific lines weren't personally re-verified in the fetch window but are near-certain given the file's purpose and corroboration from `NetcoN-OpenTether`'s `androidVIDs` list.

**systemd's `70-uaccess.rules` [V, directly fetched]**: contains `SUBSYSTEM=="usb", ENV{ID_USB_INTERFACES}=="*:060101:*", TAG+="uaccess"` — auto-grants seat-local ACL access to MTP/PTP-class interfaces (class 06/01/01) on any modern systemd distro. There's also an informational (non-uaccess) rule tagging ADB-class interfaces (`ff4201`/`ff4203`/`dc0201`) with `ID_DEBUG_APPLIANCE="android"`. **Critically**, AOA's vendor-specific accessory interface class matches none of these, so **[I]** a device in pure accessory mode (`18d1:2d00`–`2d05`) is very likely **not** automatically covered by systemd defaults — built from verified rule contents, not a directly confirmed negative. **DrawSplay should ship its own udev rule explicitly for `18d1:2d00`–`2d05`.**

**No driver binding needed on Linux [S/I]**: unlike Windows, libusb opens devices directly against the `/dev/bus/usb/BBB/DDD` node; once `uaccess`/`plugdev`+`MODE=0660` permission is granted, there's no analogous "bind a driver first" requirement — confirmed via general libusb documentation, not a dedicated libusb-Linux-permissions page.

**Kernel-driver vs. userspace conflicts [S]**: the common real-world failure is **not** a kernel driver claiming the interface (MTP/PTP has no kernel driver — it's userspace via libmtp/gvfs); it's `gvfs-gphoto2-volume-monitor`/`gvfs-mtp-volume-monitor` (or KDE equivalent) auto-claiming the device via its own libusb session, causing `LIBUSB_ERROR_BUSY`. Fix: `systemctl --user stop/disable gvfs-mtp-volume-monitor.service` or `gio mount -u`, **not** `libusb_detach_kernel_driver()` (a no-op here since there's no kernel driver to detach). This conflict window only matters during the brief pre-accessory-mode period; once re-enumerated as `18d1:2d00`, gvfs no longer recognizes the device.

**Packaging**: deb/AppImage rely purely on udev rules + group membership. **Flatpak [V]**: `xdg-desktop-portal` 1.19.1 (Dec 2024, per Phoronix) introduced a dedicated **USB portal** (Sovereign Tech Fund-funded) meant to replace the blunt `--device=all` permission with scoped, consented per-device access; the exact D-Bus interface (`org.freedesktop.portal.Usb.xml`) exists but its method signatures weren't independently confirmed this session. **Recommendation**: target `--device=all` today for broad compatibility across older portal runtimes, track the USB portal rollout for a future narrower-permission manifest.

**Hotplug [V, official libusb docs]**: `libusb_hotplug_register_callback()` exists but libusb explicitly warns *"not all platforms currently implement hotplug notification"* — must capability-check via `LIBUSB_CAP_HAS_HOTPLUG`. gousb's exposure of this API was **not confirmed** (no hotplug method surfaced in README). The one real-world Go AOA project uses simple **timed polling/backoff**, not hotplug callbacks. **Recommend polling (or udev netlink monitoring) over relying on gousb's unconfirmed hotplug surface.**

**Autosuspend**: standard sysfs mechanism (`power/control`, `power/autosuspend`) with a known udev-rule pattern to pin `power/control=on` for specific VID/PID; can be scoped tightly to `18d1:2d00`–`2d05` since those PIDs are stable. AOA-specific throughput impact wasn't separately documented **[I]**, but general USB resume-latency effects are well-established and plausibly relevant to the latency-sensitive input channel.

---

## 5. Windows: The Hard Problem

**Executive finding, directly verified [V]**: **No.** On stock Windows with zero drivers installed, a Go/libusb app cannot send *any* control transfer — not even the AOA switch requests themselves — to an arbitrary Android device in its default composite mode.

**Does Android's accessory gadget expose a Microsoft OS Descriptor (WCID) for auto-WinUSB-binding? — No [V].** Direct inspection of AOSP's `drivers/usb/gadget/function/f_accessory.c` (`android13-5.15` branch, [android.googlesource.com/kernel/common](https://android.googlesource.com/kernel/common/+/refs/heads/android13-5.15/drivers/usb/gadget/function/f_accessory.c)): grepping the full 1601-line file for `MS_OS|WINUSB|Microsoft|extended compat|0xEE|os_desc|compat_id` returns **zero matches**. The interface is a plain `bInterfaceClass=0xFF` vendor-specific interface with an ordinary string descriptor — no MS OS String Descriptor, no extended compat ID (sibling functions like RNDIS *do* use this generic plumbing; the accessory function was never wired to it). **Consequence: Windows will never auto-bind WinUSB to 18D1:2D0x; a driver install is unavoidable.**

**Does Google's own "Google USB Driver" cover the AOA PIDs? — No [V].** Direct grep of a verified mirror ([koush/UniversalAdbDriver](https://github.com/koush/UniversalAdbDriver/blob/dd8acf4c1e62cc982d3fabaa380abf413640d4f1/usb_driver/android_winusb.inf), 1,911 lines) for `2D0` → **zero matches**; the INF only covers ADB/fastboot interfaces on specific OEM PIDs. Independently corroborated: **five separate third-party GitHub projects** (`Kirea413/vmonitor`, `cfwang123/ScreenKit`, `yuvraj-dw/OpenDisplay`, `Boberborn/bobercam-android_webcamera`, `schroeder-/android_open_accessory_lib`) each had to hand-roll their own near-identical INF for `VID_18D1&PID_2D00`–`2D05` — strong convergent evidence no official solution exists.

**usbccgp.sys cannot be used as a bypass [V]**: Microsoft's own docs describe it as a purely kernel-to-kernel PnP/power multiplexer with no user-mode device interface; `IOCTL_INTERNAL_USB_SUBMIT_URB` is kernel-only. libusb's own Windows source (`libusb/os/windows_winusb.c`) logs *"The following device has no driver… libusb will not be able to access it"* for any interface with no bound driver; `libusb_open()` has no working code path otherwise. **Confirmed: at minimum one driver must be bound to the device — pre- and post-switch — before any control transfer, including the handshake itself, can be issued.**

**How real apps solve it today — uniformly manual [V]**: scrcpy's `FAQ.md`/issue #3654 points users to **Zadig** or manual Device-Manager "Have Disk" binding; it ships no driver of its own. `schroeder-/android_open_accessory_lib` ships its own unsigned INF with manual-install instructions. **No commercial display-over-USB product (Duet, Luna/Astropad, SuperDisplay, spacedesk, Vysor) was confirmed via primary source to use standard AOA** — available evidence is consistent with proprietary signed drivers or reuse of the existing ADB/MTP interface instead (flagged **[GAP]** — would need a protocol capture to settle definitively). **No precedent exists for a silent/zero-friction commercial Windows AOA deployment.**

**Practical path forward**:
1. **libwdi** ([pbatard/libwdi](https://github.com/pbatard/libwdi)) self-signs driver packages locally (not real Microsoft WHQL/attestation signing) and ships `wdi-simple.exe` for **silent, scriptable, unattended install** (`-v 0x18D1 -p 0x2D00 -t 0 -s --stealth-cert`) — this is Zadig's own underlying mechanism. **Recommended**: bundle libwdi in DrawSplay's installer, pre-provisioning WinUSB for `18D1:2D00`–`2D05` and, if ever needed, one interface of the pre-switch composite device (device-recipient control transfers can reportedly be issued once WinUSB is bound to *any* interface of a composite device — **[S]**, logical inference from WinUSB's documented device-recipient `ControlTransfer` support, not independently stress-tested).
2. **UsbDk** ([daynix/UsbDk](https://github.com/daynix/UsbDk)) bypasses the class-driver model entirely via a hub-filter driver; libusb has a runtime-selectable UsbDk backend. Ships a WHQL-signed driver (less SmartScreen friction) but libusb's own wiki explicitly discourages it for stability ("please use WinUSB driver instead") — best as an opt-in fallback, not primary.
3. Installer mechanics: `pnputil /add-driver *.inf /install` or libwdi's install APIs from an NSIS/Inno Setup/WiX custom action, with required Administrator elevation (unavoidable for driver installation regardless of method).

**Honest verdict: a driver-install step on Windows is unavoidable — there is no zero-touch path.** Budget this as real engineering + UX work (first-run elevated installer step), and flag Samsung devices specifically for extra QA given the documented driver-allowlist conflict.

---

## 6. Throughput, Transfer Sizes, ZLP, Multiplexing

**Measured throughput — concrete but discouraging [V]**: one Stack Overflow thread (summary only; page 403'd on direct fetch) reports **3.7 MB/s**, improving to **9.86 MB/s with a better cable**, against a naive AOA bulk implementation — far below the commonly-cited "20–35 MB/s" folklore figure, which **[GAP]** could not be independently verified anywhere. The low number is consistent with a synchronous, single-buffer transfer loop (exactly what the reference sample `thorikawa/android-openaccessory-with-pc-sample:pc-usb.c` does: 1024-byte buffer, one synchronous `libusb_bulk_transfer()` per loop iteration, `timeout=0`) leaving the bus idle between round trips.

**Speed class — counter-intuitive finding [V]**: direct inspection of `f_accessory.c` shows the gadget driver defines **FS, HS, SS, and SSP (SuperSpeed+)** descriptor sets (`fs_acc_descs`, `hs_acc_descs`, `ss_acc_descs`, `ssp_acc_descs`), bound per negotiated link speed via the standard gadget composite framework. **AOA is NOT capped at USB 2.0 High-Speed by kernel design** — on a SuperSpeed-capable USB-C chain, it negotiates 1024-byte max-packet bulk with burst. Whether this is realized depends on hardware (many USB-C Android ports/cables are electrically USB-2.0-only) — **architect for the HS worst case (~35–40 MB/s practical ceiling)**, where DrawSplay's 60 Mbit/s (~7.5 MB/s) target has comfortable headroom **if pipelined properly**, but is close to/above the naive-implementation floor (3.7 MB/s) — **pipelining is required, not optional, to hit the target reliably.**

**Buffer/queue sizing [V, from kernel source]**: `BULK_BUFFER_SIZE = 16384`, `TX_REQ_MAX = 4`, `RX_REQ_MAX = 2` on the Android kernel side — confirms 16KB (or a clean multiple) as the right host-side chunk size, and that the device side only queues 2 outstanding OUT (host→device, i.e. video) requests, capping how much host-side over-submission helps without a custom device-side driver.

**Async queue depth [V general guidance + I specifics]**: libusb community guidance (SourceForge mailing list threads) converges on **4–16 concurrent in-flight transfers**, resubmitted from completion callbacks, with a dedicated `libusb_handle_events()` thread. gousb exposes this via `Endpoint.NewStream(bufSize, numTransfers)` — the idiomatic Go mechanism, **recommended over hand-rolled transfer management**. Suggested starting point: `bufSize` = multiple of 16384, `numTransfers` = 4–8 for video OUT, tuned empirically (no universal magic number found).

**ZLP hazard — confirmed at the kernel-source level [V]**: `acc_write()` (device→host, input channel) **auto-generates a ZLP** when the final chunk is an exact multiple of `maxpacket` — handled for you on IN. `acc_read()` (host→device, video channel) has **no such logic** — ZLP generation is entirely the sender's (host's) responsibility. If DrawSplay writes a bulk OUT payload whose length is an exact multiple of 512 (HS)/1024 (SS) bytes without a follow-up zero-length write, the Android-side `read()` call **will block indefinitely**. **Mitigation**: after any OUT write landing on an exact multiple of `wMaxPacketSize`, issue an explicit follow-up zero-byte bulk write, or design framing (e.g., odd-length headers) to make exact-multiple payloads statistically rare while still defensively checking before every write.

**Timeouts [S]**: scrcpy uses a fixed 1000ms timeout for its (control-transfer, not bulk) HID requests — a data point, not a bulk-streaming standard. **[GAP]** no authoritative "correct" bulk timeout found; recommend a moderate finite timeout (hundreds of ms–few seconds) rather than infinite, so a stalled pipe can be detected/cancelled/reconnected.

**Multiplexing video/input [V]**: AOA provides **exactly one bulk IN and one bulk OUT endpoint** (confirmed in spec text and `acc_interface_desc.bNumEndpoints = 2` in kernel source). Since OUT (video) and IN (input) are separate physical pipes, they're naturally full-duplex and don't block each other at the USB transaction level — **no priority scheme is needed between them**; run each direction on its own goroutine with its own async queue. What *is* needed: simple length-prefixed framing **within** each direction — no standardized framing protocol exists in the open-source ecosystem beyond ad hoc length-prefix conventions.

---

## 7. Security

**No cryptographic authentication in AOA itself [V]**: the entire handshake is unauthenticated string/descriptor exchange over EP0. The only gate is Android's runtime USB permission dialog.

**"Always allow" persistence — a genuine, source-confirmed weakness [V]**: direct inspection of AOSP's `AccessoryFilter.java` ([android.googlesource.com](https://android.googlesource.com/platform/frameworks/base/+/master/core/java/android/hardware/usb/AccessoryFilter.java)) shows the persisted grant matcher has **only manufacturer, model, version strings — no serial number, no certificate, nothing cryptographic**:
```java
public boolean matches(UsbAccessory acc) {
    if (mManufacturer != null && !acc.getManufacturer().equals(mManufacturer)) return false;
    if (mModel != null && !acc.getModel().equals(mModel)) return false;
    return !(mVersion != null && !mVersion.equals(acc.getVersion()));
}
```
Since these strings are entirely attacker-chosen at connection time (via SEND_STRING), **any rogue device presenting identical strings silently inherits a previously-granted "always allow" permission with no new prompt** — a textbook BadUSB/rubber-ducky-class weakness, confirmed from source.

**Corroborating academic research [V]**: "ChoiceJacking" (USENIX Security 2025, [draschbacher et al.](https://www.usenix.org/conference/usenixsecurity25/presentation/draschbacher)) shows a malicious USB host can autonomously approve its own Android USB permission dialogs (one technique specifically leverages AOA), successfully against devices from 8 vendors including Google, Samsung, Xiaomi — a second, independent angle confirming the OS permission dialog is a weak, non-cryptographic trust boundary.

**Recommendation [I, following established precedent]**: do not rely on the OS dialog/its persistence as a security boundary beyond basic UX gating. Implement an application-layer handshake immediately after opening the bulk pipes and before trusting any data — e.g., a QR-code-seeded shared secret/token, analogous to `adb pair`/scrcpy's wireless pairing. Reject/disconnect on handshake failure or timeout.

---

## 8. Reference Open-Source Implementations

- **scrcpy** ([Genymobile/scrcpy](https://github.com/Genymobile/scrcpy)) — `app/src/usb/aoa_hid.c`/`.h`, AOA v2 HID (requests 54–57) for keyboard/mouse/gamepad-over-USB in `--otg` mode; **no bulk video path uses AOA** (OTG mode disables mirroring entirely). Good reference for control-transfer sequencing/timeout conventions, not bulk throughput.
- **`thorikawa/android-openaccessory-with-pc-sample`** (`pc-usb.c`) — minimal libusb PC-host handshake reference; synchronous 1024-byte loop is an anti-pattern to avoid, not copy.
- **`schroeder-/android_open_accessory_lib`** — C++ host-side AOA library shipping its own unsigned Windows driver INF with manual-install instructions — further evidence no official Windows solution exists.
- **`pyd-07/NetcoN-OpenTether`** (`relay/aoa_linux.go`) — most directly relevant precedent: small Go+gousb implementation of the exact handshake DrawSplay needs, OEM VID list, backoff/retry pattern.
- **Google ADK** reference firmware (`devunwired/accessory-samples`, `xin3liang/device_google_accessory_adk2012_demo`) — historical protocol corroboration only; no PC/Windows host role exists by design (the Arduino *is* the accessory-mode host).
- **ChromeOS/Chromium** — **[GAP, inconclusive not exhaustive]** no AOA-specific code found in Chromium's `device/usb/` subsystem via code search.
- **libusb itself** — no dedicated AOA example in `examples/`; its Android docs cover building libusb *for* Android as host library, not implementing AOA *from* a PC — not directly relevant.

---

## Feasibility Verdict Per OS

**Linux: feasible with moderate, well-understood effort.** No driver-install step required; libusb + uaccess/udev rules give userspace access out of the box on modern systemd distros, and a DrawSplay-shipped udev rule for `18d1:2d00`–`2d05` plus known OEM pre-enumeration VIDs covers the rest. Main work is in handshake/retry logic, async pipelining for throughput, and ZLP handling — all solvable with gousb. Packaging (deb/AppImage) is straightforward; Flatpak needs `--device=all` today or tracking the new xdg-desktop-portal USB portal.

**Windows: feasible, but only with a mandatory driver-install step — there is no zero-touch path, and claiming otherwise would be dishonest.** Verified at the kernel-source and libusb-source level: Android's accessory gadget never advertises WCID, Google's own USB driver doesn't cover the AOA PID range, and libusb on Windows cannot open any device with zero bound driver — not even to send the handshake's own control transfers. Every third-party project solving this has hand-rolled an INF and told users to run Zadig manually; none has a fully silent solution as precedent. **Honest, buildable path**: bundle libwdi (`wdi-simple`-style silent WinUSB provisioning) into the Windows installer, requiring one-time Administrator-elevated driver installation, with UsbDk as a documented opt-in fallback. Budget this as real engineering/UX work, not a footnote — flag Samsung devices for extra QA given the documented driver-allowlist conflict.

---

## Recommended Architecture

1. **Host (Go, both OSes)**: `google/gousb` as primary USB backend. Implement:
   - A probe loop over known/likely OEM VIDs and any already-present `18d1:2d00`–`2d05` device: GET_PROTOCOL → SEND_STRING(manufacturer, model, description — omit version) → START.
   - After START, stop touching the old handle; poll (100–250ms, several-second window) for the new `18d1:2d0x` device; open it, `SET_CONFIGURATION(1)`, locate first bulk IN/OUT pair.
   - Two independent goroutines (one per direction), each driving a pipelined `NewStream(16384×N, 4–8)`-style transfer, with explicit trailing zero-length writes whenever an OUT payload lands on an exact multiple of the endpoint's max packet size.
   - Simple length-prefixed framing within each direction (video NAL chunking on OUT, discrete touch/pen events on IN).
   - An application-layer handshake/token (QR-seeded shared secret) performed immediately after the pipes open, before trusting any data, independent of the OS permission dialog.
2. **Android (Kotlin)**: standard `UsbManager`/`UsbAccessory` API; `accessory_filter.xml` with manufacturer+model only; dynamic `BroadcastReceiver` for detach; foreground service with `connectedDevice` type for session longevity on Android 14+, launched from the attach-triggered Activity (not a cold background start).
3. **Linux packaging**: udev rule covering `18d1:2d00`–`2d05` (uaccess + autosuspend-off) and common OEM pre-enumeration VIDs as a first-probe aid; rely on systemd's built-in MTP-class uaccess rule for the pre-switch state otherwise.
4. **Windows packaging**: elevated installer step silently provisioning WinUSB for `18d1:2d00`–`2d05` via libwdi/`wdi-simple`, with a detectable failure path prompting toward a documented manual Zadig fallback, and explicit handling for Samsung's conflicting OEM driver.

## Pitfalls (prioritized)

1. **Windows driver binding is mandatory, not optional** — the single biggest risk to shipping a true "no developer mode" experience on Windows; don't under-scope it.
2. **Naive synchronous bulk transfer loops will not hit 60 Mbit/s** — the one concrete measurement found (3.7 MB/s) is below target; pipelined async transfers are required.
3. **ZLP omission will intermittently hang the video channel** — specifically on exact-multiple-of-512/1024-byte payloads; kernel-source-confirmed, test it explicitly.
4. **"Always allow" is not a security boundary** — implement your own handshake; don't trust the OS dialog alone.
5. **Don't filter on `version` in `accessory_filter.xml`** — causes a system reboot on Android ≤10 and an NPE pre-Android-12 if the host doesn't always send it.
6. **Don't reset/cycle the pre-switch USB handle after sending START** — can desynchronize re-enumeration (documented UsbDk issue).
7. **gvfs-gphoto2/MTP auto-mount daemons can steal the device on Linux** during the brief pre-accessory window — detect/handle `LIBUSB_ERROR_BUSY`, or proactively stop the gvfs volume monitor service.
8. **No authoritative re-enumeration timing exists** — build generous, tolerant polling, and empirically catalogue slow-to-re-enumerate devices during QA.

## Prioritized Implementation Checklist

1. Prototype the Go/gousb handshake + bulk I/O loop on Linux first (lowest-friction OS), using `pyd-07/NetcoN-OpenTether` as a starting reference; validate against 2–3 real devices from different OEMs.
2. Implement and load-test async pipelined transfer + ZLP-safe framing to confirm sustained ~60 Mbit/s OUT throughput; benchmark against the naive-loop baseline to quantify the pipelining win.
3. Build the Kotlin accessory app (manifest, filter, permission flow, 16KB-chunked I/O, detach handling, foreground service); validate the "no developer mode required" claim end-to-end on a stock, non-developer-mode device.
4. Ship Linux udev rules (uaccess + autosuspend-off for `18d1:2d0x`) and validate deb/AppImage packaging; evaluate Flatpak portal readiness on target distros.
5. Build and test the Windows libwdi/`wdi-simple` silent driver-provisioning installer step; specifically validate against a Samsung device with official Samsung drivers pre-installed (the documented failure case), and confirm the Zadig-equivalent fallback UX.
6. Add the application-layer pairing/handshake (QR-seeded token) before trusting any input/video data over the pipe.
7. Empirically catalogue OEM-specific quirks (slow re-enumeration, MIUI/EMUI interference) during multi-device QA, since no authoritative external list exists.
