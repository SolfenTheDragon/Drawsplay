---
title: SuperDisplay Comparison and ADB-Free Transport
type: topic
tags: [transport, usb, aoa, wifi, adb, developer-mode, competitor-analysis]
created: 2026-08-12
updated: 2026-08-12
sources:
  - FUNCTIONALITY.md
  - wiki/topics/transport-and-session-model.md
summary: Static analysis of the SuperDisplay Android app's transports, how they differ from DrawSplay's adb-reverse design, and how DrawSplay can drop the developer-mode requirement.
---

# SuperDisplay Comparison and ADB-Free Transport

## Scope and evidence quality

- **Analyzed:** `com.kelocube.mirrorclient` v1.2.25 (versionCode 70) from the APKM, by static analysis of the manifest and dex bytecode (androguard). 
- **Not recovered:** the Windows installer `SuperDisplay_Installer_v1.1.10.exe` is an Advanced Installer bootstrapper. Its readable parts are VC++ redistributables and prerequisite MSIs. The actual host application payload is in an obscured ~8 MB blob that could not be unpacked statically. **Everything below about the PC side is therefore inference** from the Android client's behavior and the Android Open Accessory spec.
- Items marked *(observed)* come straight from the APK; *(inferred)* items are not verified.

## How SuperDisplay talks without adb

### Transport 1: USB via Android Open Accessory (AOA) *(observed on the tablet side)*

- Manifest declares `uses-feature android.hardware.usb.accessory` (not required), `uses-library com.android.future.usb.accessory`, and an exported `IntentReceiverActivity` handling `android.hardware.usb.action.USB_ACCESSORY_ATTACHED` with an accessory filter XML resource.
- No `USB_DEVICE` host-mode code and no adb dependency. `AOAPTransportListener` uses `UsbManager.openAccessory()`, obtains a `ParcelFileDescriptor`, and wraps it as in/out streams (`AOAPPipe`). It registers a `USB_ACCESSORY_DETACHED` receiver and a `com.android.example.USB_PERMISSION` broadcast.
- Meaning: the **PC is the USB host** and switches the tablet into accessory mode (*inferred*: USB control requests 51/52/53 on the tablet with vendor/product re-enumeration to Google VID `0x18d1`, PID `0x2d00`-`0x2d05`, via libusb/WinUSB). The tablet then exposes a bulk IN/OUT pair. The user only has to tap "allow" once; **developer mode and USB debugging are not needed**.

### Transport 2: LAN TCP, tablet is the server *(observed)*

- `TCPTransportListener` binds a `ServerSocketChannel` on **TCP 1445** (accept timeout 500 ms), logging "Accepted TCP client from ...". The PC connects to the tablet (the reverse of DrawSplay's Wi-Fi mode, where the host listens).
- A `BroadcastReceiver` tracks `android.net.wifi.STATE_CHANGE` to start/stop the listener.
- `HostPoller`: the tablet periodically opens a TCP connection to a known host address purely to learn whether the PC app is reachable (poll every 10 s on success, 2 s on failure, 10 s connect timeout). No discovery protocol (mDNS/NSD) was seen in the dex class list, so pairing is *probably* by IP/host entry (*inferred*).

### Shared session protocol (`Transport`) *(observed)*

Both transports feed the same `Transport` abstraction, so protocol code is transport-agnostic.

| Item | Value |
|---|---|
| Handshake | ASCII `KELOCUBE_MIRR_` + 3-digit zero-padded version; client replies with the same prefix + its version + space |
| Supported host versions | 3 and 4 (`SUPPORTED_HOST_VERSIONS`); errors for old host / old client / wrong protocol |
| Framing | Binary packets, byte-order-aware buffers; incoming size validated ("Packet too large"), multiple streams (`readStreams`) |
| Buffers | read 100 000 B and 10 000 000 B; send 10 000 B (outgoing packets > 10 000 B rejected) |
| Liveness | 10 ticks x 1000 ms without data => "Read timed out"; separate read and send threads |
| Input | per-pointer flags `CONTACT=1`, `HOVER=2`, `BUTTON=4`; 50 ms tick, 50 ms min repeat; Samsung S-Pen actions (211-214) and a pen emulator |
| Video | `MediaCodec` decoders (`AsyncDecoder`, `SyncDecoder`); software-encoding flag; output-resolution modes 254/255 (scale to / copy source) |
| Errors reported by host | driver, license, trial expired, bad resolution, GPU driver, no encoder, software-encoder warning |

## Differences from DrawSplay

| Area | SuperDisplay | DrawSplay today |
|---|---|---|
| Primary USB path | AOA accessory mode (PC = USB host, bulk pipe) | `adb reverse` to host loopback `27183`/`27184` |
| Needs developer mode / USB debugging | No | **Yes** for the default path |
| Host-side USB dependency | Own USB stack (libusb/WinUSB-class driver, *inferred*) | `adb` binary (bundled or downloaded) |
| Wi-Fi roles | Tablet listens on 1445; PC dials in | Host listens (TLS + token); tablet dials via NSD/DNS-SD |
| Wire format | Binary, versioned `KELOCUBE_MIRR_nnn` handshake | Newline-delimited JSON input, framed Annex-B NAL video |
| Discovery | Poll of a known host (no NSD seen) | DNS-SD `_drawsplay._tcp` + NSD |
| Security | Not observed (no TLS/token visible in the transport classes) | TLS + shared token on Wi-Fi and USB Link |
| Non-adb options already present | n/a | Wi-Fi (feature flag), USB Link (IP-over-USB NIC, token required) |

The key point: DrawSplay already has two adb-free transports, but they are not the default and USB Link needs the user to enable USB tethering (which on stock Android lives in Settings, not developer options).

## How DrawSplay can stop requiring adb / developer mode

### Option A (recommended for USB): AOA transport

1. **Android:** add `<uses-feature android:name="android.hardware.usb.accessory" android:required="false"/>`, an exported activity (or the existing main activity) with an intent filter for `USB_ACCESSORY_ATTACHED` and `res/xml/accessory_filter.xml` (manufacturer `DrawSplay`, model `Tablet`, version `1`). Open it with `UsbManager.openAccessory`; run reads/writes on background threads (no main-thread I/O).
2. **Reuse the existing mux:** `transport/UsbMuxFrameCodec.kt` already frames multiple channels over one byte pipe. Run the same input and video channels over the accessory `FileInputStream`/`FileOutputStream` instead of two TCP sockets. Keep the JSON protocol unchanged (additive `transport` field only).
3. **Host:** add a Go USB module (`usb_aoa_*.go` with `_other.go` stub) that enumerates devices, sends AOA `GET_PROTOCOL`, the identification strings (`SEND_STRING` index 0-5) and `START`, waits for re-enumeration as `18d1:2d00`/`2d01`, claims the bulk endpoints, and bridges them to the same in-process handlers used by the loopback listeners. Needs libusb (via cgo or a pure-Go binding) on Linux and Windows.
4. **Platform setup:** Linux needs a udev rule (`SUBSYSTEM=="usb", ATTR{idVendor}=="18d1", MODE="0660", TAG+="uaccess"` plus the tablet vendor's pre-switch ID or a broad rule). Windows needs the WinUSB driver for the accessory interface (Zadig-style install by the signed installer; no user-visible developer mode).
5. **Priority order:** AOA -> (existing) adb reverse when present -> Wi-Fi -> USB Link. `adb reverse` stays as a fallback so existing users do not regress, per the project's USB-over-ADB rule.
6. **Latency:** keep coalescing of stale moves; AOA bulk transfers at up to 480 Mbit/s are enough for the 60 Mbps ladder cap, and the mux must keep video and input in separate priority queues so a large IDR cannot delay a pen event.

### Option B (least work): promote Wi-Fi and USB Link to defaults

- Wi-Fi: enable `DRAWSPLAY_FEATURE_WIFI` by default with a first-run QR/pairing flow carrying the token and host fingerprint; no developer mode required. Trade-offs: latency and jitter higher than USB.
- USB Link: document and detect USB tethering. Add a tablet-side hint when the tethering NIC appears. Works without developer mode but needs the user to toggle tethering.

### Option C: tablet-as-server (SuperDisplay style)

Possible but not recommended: it forces the host to know the tablet address and complicates NAT/firewalls. The existing host-listens model with NSD discovery is more robust.

## Recommended plan

1. Ship Option B quickly (default-on Wi-Fi pairing) as the no-dev-mode path.
2. Implement Option A as the low-latency wired path, reusing `UsbMuxFrameCodec`.
3. Keep adb as an opt-in fallback; update `FUNCTIONALITY.md`, `README.md` (setup without developer mode), and `BUILD.md` (libusb, udev/WinUSB) when implemented.

## Open questions for verification

- Unpack the Windows payload (run the installer in a VM, or capture the installed files) to confirm the host-side USB stack, ports, and whether pen/touch uses a virtual HID or a custom driver.
- Decode the accessory filter resource (`@7F150006`) to confirm the manufacturer/model strings.
- Capture traffic (USB or TCP 1445) to confirm packet types, video codec, and whether any encryption is used.
