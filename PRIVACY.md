# DrawSplay Privacy Policy

_Last updated: 2026-11-11_

DrawSplay turns an Android tablet into a touch/pen-enabled secondary display for a Windows or Linux computer. It is built to work locally: no account is required, and DrawSplay does not collect, sell, or share personal data.

## Summary

- DrawSplay has **no accounts, analytics, telemetry, crash reporting, or advertising**.
- Your screen content and input stay between your computer and your tablet.
- Nothing is sent to the DrawSplay developers.

## What DrawSplay processes

| Data | Purpose | Where it goes |
|------|---------|---------------|
| Screen contents of the virtual (or mirrored) display, encoded as H.264/HEVC video | Show your desktop on the tablet | Computer → tablet only, over USB (default) or, if you enable it, your local Wi-Fi network |
| Touch, pen (position, pressure, tilt, buttons) and keyboard input | Control the computer from the tablet | Tablet → computer only; injected into the computer's input system |
| Decode statistics (frame/queue counts) | Adapt video bitrate | Tablet → computer only |
| Device identifiers (USB serial, vendor/product ID, tablet display size) | Recognize the tablet, remember per-device display settings, install USB drivers/rules | Stored locally on your computer |

## What is stored locally

On the computer, DrawSplay stores settings (bitrate, resolution, input toggles, per-tablet display preferences and saved pen calibration), paired Wi-Fi keys and logs in your per-user config directory (`%APPDATA%\DrawSplay` on Windows, `~/.config/DrawSplay` on Linux). Calibration target taps are processed only in browser memory; saving stores four scale/offset coefficients, not a pen-input history. The settings page stores its appearance choice in this browser's local storage.

Android stores local display/scaling preferences, per-host resolution and refresh-rate choices, UI/migration flags, update-check timestamps, and downloaded update APKs. Optional Wi-Fi pairing stores an encrypted host public-key fingerprint and label in app-private, no-backup storage, with non-exportable identity/encryption keys in Android Keystore. Forgetting a host removes its pairing record, not the tablet's identity keys. Expandable setup help is not persisted. The app does not keep your screen content or input history.

DrawSplay Desktop stores paired public keys and user-chosen device names in its existing local trust files; it does not save launcher host addresses or display choices as new preferences. These data are not sent to analytics or cloud services. You can delete local configuration data by removing the relevant application configuration; Windows uninstall offers to keep or delete its settings.

## Network access

- The settings web page listens only on localhost (`127.0.0.1`). The tablet reaches the host over USB (USB accessory mode, no network port); only the optional ADB tunnel opens two localhost ports, and only while it is enabled.
- **Wi-Fi mode is off by default.** If you enable it and pair a tablet with the code shown on the computer, the tablet advertises itself on your local network via DNS-SD (`_drawsplay-tablet._tcp`) and the host connects to it over mutually authenticated TLS, using the keys exchanged during pairing. Traffic stays on your local network.
- On Windows only, the installer downloads the newest ffmpeg release compatible with your Windows version and CPU architecture from GitHub (gyan.dev essentials builds, or BtbN builds as a fallback / for ARM64), verifying the SHA-256 published with the release. The host does the same at runtime if ffmpeg is missing and automatic tool download is enabled. adb is never downloaded. Your IP address is visible to those servers as with any download; no DrawSplay data is sent. You can disable automatic downloads in the settings page.
- If the preferred Windows virtual-display driver fails and Parsec VDD is not already installed, elevated setup may download the pinned Parsec VDD installer from `builds.parsec.app` (and its HTTPS download redirects). It verifies the pinned SHA-256 and publisher signature before running it. Those servers see your IP address; no screen or input data is sent. Disabling automatic tool installation or using `-no-driver-install` prevents this download.
- **Update checks.** While the settings page is open, the host asks GitHub's API (`api.github.com`) for the latest DrawSplay release, at most every 6 hours, and when you click "Check now". Only a standard HTTPS request is sent (with a `DrawSplay-host/<version>` user agent); GitHub sees your IP address. Turn this off with "Check for DrawSplay updates automatically" in Advanced settings. Updates are downloaded from GitHub only when you click "Install", and are verified against the release's Ed25519-signed SHA-256 checksums using a pinned public key.
- **Tablet app updates.** When the Android app is opened, it asks GitHub's API for the latest DrawSplay release, at most every 6 hours. If there is a newer one, it downloads `DrawSplay.APK` from GitHub (by itself on unmetered Wi-Fi, otherwise when you tap), verifies it, and installs it only when you tap Install. GitHub sees your IP address; no DrawSplay data is sent.
- On Linux, the install script may use your package manager (`apt`) to install dependencies.

## Permissions

- **Android:** USB accessory access, and (only if Wi-Fi mode is used) local network access. These are used solely to connect to your computer. The app may also ask to "install unknown apps", which it uses only to install DrawSplay updates you tap Install for.
- **Computer:** the installer may add udev rules and `plugdev` group membership, install a virtual display driver (Windows), and register the host to start at login. Linux input injection uses a session-scoped `uaccess` rule for `/dev/uinput`; the installer does not add you to the `input` group. On Linux X11 the host automatically tries loading `vkms`, subject to crash/kernel guards, then may switch an unplugged HDMI port on through `/sys/class/drm`. Either can require one password prompt when a tablet connects. A forced port stays on until reboot or reset; neither method persists across reboots. `DRAWSPLAY_X11_VKMS_AUTOLOAD=0` disables module loading; `1` explicitly overrides the crash guards and can crash the desktop session. These permissions are used only to create the virtual display, capture it, and inject your input.

## Third parties

DrawSplay does not share data with third parties. It uses open-source components (such as ffmpeg, GStreamer, and DrawSplay's bundled IddCx virtual display driver on Windows) that run locally on your computer.

## Children

DrawSplay does not collect personal information from anyone, including children.

## Security

Communication is limited to localhost/USB by default; paired Wi-Fi uses mutually authenticated TLS with paired-device keys. DrawSplay Desktop uses an independently paired, encrypted PC-to-PC connection when its host is explicitly bound for LAN access. The settings page requires a per-process anti-CSRF token and a loopback origin. Keep your computer and tablet up to date and only enable network connections on networks you trust.

## Changes to this policy

Changes will be published in this file in the project repository, with the date above updated.

## Contact

Questions or concerns: open an issue at <https://github.com/SolfenTheDragon/Drawsplay/issues>.
