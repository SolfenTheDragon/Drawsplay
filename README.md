# DrawSplay

Use your Android tablet as a touch- and pen-enabled extension of your computer's desktop. DrawSplay streams the desktop to the tablet and sends touch and stylus input back to the computer.

Download the latest [DrawSplay release](https://github.com/SolfenTheDragon/Drawsplay/releases/latest). This guide covers installing and using its packaged app and host:

- **Host:** `DrawSplay.exe`
- **Android app:** `DrawSplay.APK`

No source build is required.

## Before you begin

### Windows host PC

1. Windows 10/11
2. A USB cable between host and tablet
3. Internet access on first run if `adb`/`ffmpeg` are not already present and not bundled

### Android tablet

1. Android device with Developer Options enabled
2. USB debugging enabled

---

## Install the host and app

Place the binaries somewhere easy to find, for example:

`C:\DrawSplay\`

Expected files:

- `C:\DrawSplay\DrawSplay.exe`
- `C:\DrawSplay\DrawSplay.APK`

Optional prebundled runtime files (recommended for offline setup):

- `C:\DrawSplay\runtime\adb\adb.exe`
- `C:\DrawSplay\runtime\ffmpeg\ffmpeg.exe` (plus ffmpeg DLLs)

---

### Install the Android app

Connect the tablet over USB, then run:

```powershell
adb devices
adb install -r C:\DrawSplay\DrawSplay.APK
```

If prompted on the tablet, allow USB debugging for this computer.

---

### Start the host

In PowerShell:

```powershell
cd C:\DrawSplay
.\DrawSplay.exe
```

What happens on first run:

1. Host starts local listeners for input/video.
2. Host keeps `adb reverse` mappings active for USB transport.
3. If host settings still use defaults (`adb`/`ffmpeg`), tool resolution is automatic:
   - Prefer bundled runtime copies under `runtime\`
   - Otherwise use PATH / known install locations
   - Otherwise auto-download and install bundled runtime copies beside `DrawSplay.exe`
4. On Windows, if Parsec VDD is missing and auto-install is enabled, the host may attempt a driver install (UAC prompt may appear).

---

### Connect your tablet

Open the DrawSplay app on the tablet.

Connection behavior is automatic:

1. App connects over USB tunnel (`adb reverse`)
2. Host creates/uses target display path
3. Tablet shows host desktop stream and sends touch/pen input back to host

No manual IP entry or pairing is required.
When Wi-Fi transport is enabled on the host, the tablet keeps the known-host trust from host policy sync so future launches can reconnect over Wi-Fi without asking for a new code.

---

## Adjust settings

While host is running, open:

`http://127.0.0.1:27180`

You can change:

1. Bitrate
2. Input toggles (touch/pen)
3. Tool paths (`adb`, `ffmpeg`)
4. Session controls (disconnect active tablet session, temporary input lock)
5. Downloadable diagnostics snapshot for troubleshooting
6. First-run readiness checklist and live health counters
7. Setup repair action (restarts ADB checks) and video reconnect control
8. Windows VDD custom-resolution admin setup action (one-time UAC)

Video-setting changes restart the active stream automatically.

On **Windows with the Android app connected**, resolution and refresh rate are controlled from **Windows Display Settings** (not from DrawSplay web settings).

---

## Daily use

1. Connect tablet over USB
2. Start `DrawSplay.exe`
3. Launch DrawSplay app on tablet

That is the full daily startup sequence.

---

## Troubleshooting

### Tablet not listed by ADB

Run:

```powershell
adb kill-server
adb start-server
adb devices
```

If no global `adb` command exists, start `DrawSplay.exe` once and let it provision bundled tools, then use:

```powershell
C:\DrawSplay\runtime\adb\adb.exe devices
```

Then reconnect USB and confirm USB debugging prompt is accepted.

### Black screen or no video

1. Verify ffmpeg is available (global or bundled):
   ```powershell
   ffmpeg -version
   ```
   If global ffmpeg is not installed, check bundled copy:
   ```powershell
   C:\DrawSplay\runtime\ffmpeg\ffmpeg.exe -version
   ```
2. Keep host and tablet app both open.
3. Try a lower stream load from web UI (for example, 60 fps with a lower bitrate and moderate resolution).

### No extend display on Windows

If virtual display driver setup did not complete:

1. Restart `DrawSplay.exe` as a normal desktop user with UAC approval.
2. Reboot if Windows requests it after driver installation.

If native/custom resolutions do not appear in Windows Display Settings:

1. Open `http://127.0.0.1:27180`.
2. Run **Enable custom-resolution registry access (Admin)** once.
3. Reconnect the tablet/session so the host can republish the native mode.

---

## Connection and privacy

- Host listeners bind to localhost and rely on USB `adb reverse`.
- The settings page is localhost-only.
- Keep USB debugging enabled only on trusted host PCs.
