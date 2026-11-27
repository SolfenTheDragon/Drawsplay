# DrawSplay

**Use an Android tablet as a pen and touch screen for your Windows or Linux computer.** DrawSplay streams an extra desktop to the tablet and sends your touch and stylus input back, with pressure, tilt and pen buttons. Plug in a USB cable and it connects: no IP addresses, pairing codes, or USB debugging.

> [!IMPORTANT]
> The only official source of DrawSplay is this repository's [Releases page](https://github.com/SolfenTheDragon/Drawsplay/releases/latest). See [Verify your download](#verify-your-download).

**Contents:** [Features](#features) · [Will it work for me?](#will-it-work-for-me) · [Download](#download) · [Install](#install) · [Use](#use) · [Troubleshooting](#troubleshooting) · [Wi-Fi (beta)](#wi-fi-beta) · [Privacy](#privacy-and-network-access) · [Documentation](#documentation) · [Contributing](#help-and-contributing)

## Features

- **Extended desktop** on the tablet, or mirroring when a virtual display isn't available
- **Pen input** with pressure, tilt and buttons, plus touch and multi-touch
- **Plug and play over USB:** reconnects by itself after drops and unplugging
- **Wi-Fi** (beta, opt-in) for cable-free use, encrypted and limited to paired devices
- **Tablet's native resolution** by default, remembered per computer, at 60 fps
- **No accounts, telemetry or ads**

## Will it work for me?

| Your computer | Extra display | Pen and touch | Notes |
|---|---|---|---|
| Windows 10 (1903+) / 11, x86-64 | Yes | Yes | Installer adds a virtual display driver |
| Linux: GNOME, KDE Plasma 6, sway, Hyprland (Wayland) | Yes | Yes | |
| Linux: X11 | When the GPU has an unused output, or `vkms` loads | Yes | Otherwise mirrors your screen |
| Other Linux desktops | Mirror only | Yes | |
| macOS, ARM computers | Not supported | | |

- **Tablet:** Android 8 or later. Developer options and USB debugging aren't needed.
- **Cable:** a USB data cable between computer and tablet.
- **Windows:** internet access while installing (the installer downloads ffmpeg).
- **Linux:** an x86-64 computer with ffmpeg.

## Download

From the [latest release](https://github.com/SolfenTheDragon/Drawsplay/releases/latest):

| You use | Download |
|---|---|
| Windows | `DrawSplay-windows-vX.Y.Z.zip` (installer and tablet app) |
| Linux | `DrawSplay-linux-vX.Y.Z.tar.gz` (program, install scripts and tablet app) |
| Tablet app only | `DrawSplay.APK` |

`DrawSplayDesktop-…` files are a separate tool for using a *PC* as the second screen; see [DrawSplay Desktop](#drawsplay-desktop).

### Verify your download

Each release includes `SHA256SUMS.txt`, signed in `SHA256SUMS.sig` with DrawSplay's release key. The built-in updater checks this signature itself; to check a manual download on Linux (or anywhere with OpenSSL 3):

```bash
# 1. Save DrawSplay's release public key
cat > drawsplay-release.pub <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAJHlg7aDiw1KsEVrJkavMRfzMqF+0KbwOWlRCbtSuyPE=
-----END PUBLIC KEY-----
EOF

# 2. Check the signature, then the files you downloaded
openssl pkeyutl -verify -pubin -inkey drawsplay-release.pub -rawin \
  -in SHA256SUMS.txt -sigfile SHA256SUMS.sig
sha256sum --check --ignore-missing SHA256SUMS.txt
```

You should see `Signature Verified Successfully` and `OK` next to each file. On Windows, compare `Get-FileHash DrawSplay-windows-vX.Y.Z.zip` with the line in `SHA256SUMS.txt`.

## Install

### Windows

1. **Tablet:** copy `DrawSplay.APK` to the tablet, open it, and allow installs from that source when Android asks.
2. **Computer:** extract the zip and run `drawsplay_installer_vX.Y.Z.exe`.
   - Choose a desktop shortcut and whether to start at sign-in.
   - Keep **Enable an extra screen** on to install the virtual display driver, or turn it off for mirroring only.
   - Click **Install** and approve the administrator prompt once.
3. DrawSplay runs in the **system tray**. Click the tray icon for the menu; double-click it for settings.

> [!NOTE]
> Windows may warn you before running the installer. If SmartScreen shows **Windows protected your PC**, choose **More info → Run anyway**. Some antivirus tools also flag the installer because it installs drivers and a local signing certificate (see [what the installer changes](#what-the-installers-change)). Check your download first, and never turn off your antivirus to install DrawSplay.

Silent install: `drawsplay_installer_vX.Y.Z.exe -install -quiet` (add `-desktop-shortcut`, `-no-autostart` or `-no-driver-install` as needed).

**Uninstall** from **Apps & Features** or the **Uninstall DrawSplay** Start Menu shortcut. To keep your settings, run `DrawSplay-Uninstall.exe -keep-settings`.

### Linux

1. **Computer:** extract the download and run the installer as your normal user (not with `sudo`):
   ```bash
   tar -xzf DrawSplay-linux-*.tar.gz
   cd DrawSplay-linux-*/
   ./install-drawsplay-host-linux.sh
   ```
   It asks for your password once to set up USB and input permissions. Log out and back in if it asks you to.
2. **Tablet:** copy `DrawSplay.APK` to the tablet, open it, and allow installs from that source when Android asks.

For smoother streaming, install your GPU's VA-API driver: `intel-media-va-driver-non-free` (Intel) or `mesa-va-drivers` (AMD).

**Uninstall:** run `./uninstall-drawsplay-host-linux.sh` from the extracted folder. Your settings in `~/.config/DrawSplay` are kept; add `--purge` to delete them too.

### What the installers change

DrawSplay needs system access to create a display and type, click and draw for you. It's worth knowing what that means:

- **Windows:** installs the DrawSplay virtual display driver and USB drivers. To sign those drivers on your PC it trusts a new per-PC certificate and deletes its private key right away. The uninstaller removes the drivers and certificates. If the bundled display driver can't be used, setup may download the Parsec virtual display driver (pinned version, verified); `-no-driver-install` prevents both.
- **Linux:** installs the program to your user account, adds it to your menu and autostart, and installs missing packages on Ubuntu/Debian. It adds udev rules for the tablet's USB connection and for `/dev/uinput`. That rule lets programs **in your logged-in session** create input devices, so they can simulate a keyboard, mouse or pen; it doesn't add you to the `input` group. A polkit helper lets DrawSplay switch on a virtual or unused display output when the tablet connects, and lets the troubleshooting buttons in the settings page install a missing rule or package, after asking for your password.

The uninstallers remove these changes. Details are in [PRIVACY.md](PRIVACY.md#permissions).

## Use

1. Connect the tablet by USB. The first time, accept the prompt on the tablet.
2. Start DrawSplay on the computer (it starts at sign-in if you chose that).
3. Open DrawSplay on the tablet.

The tablet shows the extra desktop and sends touch and pen input back. While the desktop is shown, swipe in from a screen edge and press **Back** for **Connection options**.

**Settings:** open `http://127.0.0.1:27180` or **Settings** in the tray menu for scaling, input toggles, pen calibration, connection health and diagnostics. See [docs/settings.md](docs/settings.md).

**USB file transfer:** while DrawSplay runs it uses the tablet's USB connection, so *File transfer* mode isn't available. On Linux, pick *File transfer* on the tablet to move files, then *No data transfer* to return to DrawSplay. On Windows, quit DrawSplay to transfer files. To switch only when you choose, turn off **Switch the tablet to accessory mode automatically** in the settings and use **Switch to USB accessory mode** when you want DrawSplay.

## Troubleshooting

The settings page's **Troubleshooting** section shows setup checks and can save a diagnostics file to attach to an issue.

| Problem | Try this |
|---|---|
| Tablet doesn't connect | Unplug and replug the cable and accept the prompt on the tablet. Then run the installer again (Windows), or the install script followed by logging out and back in (Linux). |
| Linux: pen or touch does nothing | Run the install script again, then log out and back in. |
| Linux: mouse cursor disappears | Press Ctrl+Alt+Del and move the mouse. Turning off your desktop's "hide the pointer while typing" setting prevents it. |
| Linux: mirror instead of an extra display | See [below](#linux-mirror-instead-of-an-extra-display). The settings page shows the reason. |
| Windows: no extra display | Run the installer again and restart Windows if it asks. |
| Black screen or no video | Keep both apps open and try a lower bitrate or resolution in the settings. |
| Video lags on an older tablet | Set the tablet display to the **Recommended maximum for this tablet** shown in the settings. |
| Multi-touch doesn't work in an X11 app | Some apps need touch turned on; for Firefox, start it with `MOZ_USE_XINPUT2=1 firefox`. |
| Tablet app doesn't open when plugged in | Turn on **Open the tablet app when USB connects** in the settings, or open it yourself. |

#### Linux: mirror instead of an extra display

- **Wayland:** use GNOME, KDE Plasma 6, sway or Hyprland.
- **KDE Plasma:** the tablet is a virtual display whose resolution you set in DrawSplay's settings. As a backup, turn on **Show the tablet on a spare HDMI port** in the settings: with an unused HDMI port, the tablet then shows up in Display Configuration as a monitor called **DrawSplay**, and you pick its resolution there. DrawSplay asks for your password once per tablet size to install the tablet's display description, and uses the virtual display when no HDMI port is free. The option needs you in the `plugdev` group (`sudo groupadd -f plugdev && sudo usermod -aG plugdev $USER`, then log in again), so DrawSplay can switch the port off again without a password; otherwise it keeps using the virtual display.
- **X11:** DrawSplay tries the `vkms` virtual display module, then an unused HDMI port, then mirroring. It may ask for your password when the tablet connects. `vkms` loading is blocked automatically after crashes or known kernel problems. `DRAWSPLAY_X11_VKMS_AUTOLOAD=0` turns it off.

> [!CAUTION]
> `DRAWSPLAY_X11_VKMS_AUTOLOAD=1` overrides those crash guards and can crash your desktop session. Don't use it as a routine fix.

## Wi-Fi (beta)

Wi-Fi works for display, touch and pen, but it's in beta and has more delay than USB. When both are connected, USB is used.

1. In the settings, turn on **Wi-Fi**, choose your network and save. A pairing code appears in **Host overview**.
2. On the tablet, tap **Wi-Fi connect** and enter the eight digits. On Android 17 and later, allow the **Nearby devices** permission when Android asks; Wi-Fi can't reach your computer without it.
3. When the tablet says the code is verified, click **Approve** on the computer.

The tablet then reconnects to that computer by itself. Connections are encrypted and only paired devices are accepted. For the best picture, use a 5 GHz network with a strong signal (**Host overview** shows the signal strength).

**Switch to a different computer:** on the tablet's waiting screen, tap **Forget wireless host**, confirm, then pair with the new computer. If the desktop is showing, use **Back → Connection options** first.

**If Wi-Fi doesn't connect,** check these in order:

- Unplug the USB cable (USB takes priority).
- In DrawSplay's settings, Wi-Fi is on and the right network is chosen.
- Both devices are on the same network, not a guest network. Guest and "isolated" networks stop devices from seeing each other.
- No VPN is running on either device.
- On Windows, the network is set to **Private**, not Public. The tablet is found with local network discovery (mDNS), which Public networks may block.

## Privacy and network access

DrawSplay has no accounts, analytics, telemetry or ads, and sends nothing to its developers. Your screen and input go only between your computer and your tablet, over USB or (if you turn it on) your local Wi-Fi.

DrawSplay does contact GitHub and other download servers for these:

- **Update checks:** the computer and the tablet app each ask GitHub for the latest release at most every 6 hours. Updates install only when you tap or click **Install**, after they are checked against the signed checksums. Turn off the computer's checks with **Check for DrawSplay updates automatically** in the settings.
- **Tool downloads (Windows):** the installer downloads ffmpeg, and may download a fallback display driver (Parsec VDD) if the bundled one fails. Both can be turned off in the settings or with `-no-driver-install`.

GitHub and those servers see your IP address, as with any download. Details: [PRIVACY.md](PRIVACY.md).

## DrawSplay Desktop

DrawSplay Desktop is a separate tool that turns one **computer** into an extra display for another (Windows and Linux in any combination), with mouse, keyboard, touch and pen sent back. It doesn't use the tablet app. Download `DrawSplayDesktop-…` from the release page and see the [DrawSplay Desktop README](desktop/README.md) and [guide](desktop/GUIDE.md).

```sh
dd-host --bind YOUR_LAN_IP:47800   # on the computer to extend
dd-client                          # on the computer that becomes the display
```

> [!WARNING]
> A paired computer can type, click and draw on the host as you. Approve pairing only when the codes on both screens match, and only for computers you trust.

## Documentation

- [Settings](docs/settings.md): the settings page, pen calibration, resolution
- [Privacy](PRIVACY.md): what DrawSplay stores and connects to
- [Security](SECURITY.md): report vulnerabilities privately
- [DrawSplay Desktop guide](desktop/GUIDE.md): PC-to-PC setup and pairing
- [Building from source](BUILD.md): toolchains, builds and packaging
- [Functionality reference](FUNCTIONALITY.md): components and protocol
- [Contributing](CONTRIBUTING.md)

## Help and contributing

- **Bugs and questions:** [open an issue](https://github.com/SolfenTheDragon/Drawsplay/issues). Attach the diagnostics file from the settings page if you can.
- **Security problems:** report them privately as described in [SECURITY.md](SECURITY.md), not in public issues.
- **Contributions** are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first, and open an issue before starting anything larger than a small fix.

The code is organised as:

| Folder | What it is |
|---|---|
| `host/` | The computer program (Go): capture, encoding, USB and Wi-Fi transport, input injection, settings page |
| `android/` | The tablet app (Kotlin) |
| `native/` | Optional native capture/encode helper |
| `drivers/` | Windows virtual display driver (IddCx) |
| `desktop/` | DrawSplay Desktop (Rust) |
| `packaging/`, `scripts/` | Installers, udev rules and release scripts |

See [BUILD.md](BUILD.md) to build everything from source.

## License

DrawSplay is licensed under the [GNU General Public License v3.0](LICENSE).

The bundled Windows virtual display driver is derived from Microsoft's IddCx sample under the Microsoft Public License (MS-PL); see [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
