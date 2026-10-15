# DrawSplay Privacy Policy

_Last updated: 2026-10-15_

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

On the computer, DrawSplay stores settings (bitrate, resolution, input toggles, per-tablet display preferences, optional Wi-Fi token) and logs in your per-user config directory (`%APPDATA%\DrawSplay` on Windows, `~/.config/DrawSplay` on Linux). The Android app does not keep your screen content or input history. You can delete this data at any time by removing those folders; uninstalling the host keeps them until you do.

## Network access

- The host listens only on localhost (`127.0.0.1`) for the tablet channels and the settings web page. The tablet reaches them over USB.
- **Wi-Fi mode is off by default.** If you enable it, the host listens on your local network, protected by TLS and a pairing token, and advertises itself via DNS-SD (`_drawsplay._tcp`). Traffic stays on your local network.
- On Windows only, if `ffmpeg` or `adb` is missing and automatic tool download is enabled, the host downloads pinned, checksum-verified copies from their public distribution sites. Your IP address is visible to those servers as with any download; no DrawSplay data is sent. You can disable automatic downloads in the settings page.
- On Linux, the install script may use your package manager (`apt`) to install dependencies.

## Permissions

- **Android:** USB accessory access, and (only if Wi-Fi mode is used) local network access. These are used solely to connect to your computer.
- **Computer:** the installer may add udev rules and group membership (`input`, `plugdev`), install a virtual display driver (Windows) or load the `vkms` module (Linux X11), and register the host to start at login. These are used only to create the virtual display, capture it, and inject your input.

## Third parties

DrawSplay does not share data with third parties. It uses open-source components (such as ffmpeg, GStreamer, and the Parsec VDD virtual display driver on Windows) that run locally on your computer.

## Children

DrawSplay does not collect personal information from anyone, including children.

## Security

Communication is limited to localhost/USB by default; Wi-Fi mode uses TLS with a token. The settings page requires a per-process anti-CSRF token and a loopback origin. Keep your computer and tablet up to date and only enable Wi-Fi mode on networks you trust.

## Changes to this policy

Changes will be published in this file in the project repository, with the date above updated.

## Contact

Questions or concerns: open an issue at <https://github.com/SolfenTheDragon/Drawsplay/issues>.
