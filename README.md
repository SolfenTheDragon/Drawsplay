# DrawSplay

Use your Android tablet as a touch- and pen-enabled extension of your computer's desktop. DrawSplay streams your desktop to the tablet and sends touch and stylus input back to the computer.

## Features

- Extended desktop on the tablet (mirroring when a virtual display isn't available)
- Touch, multi-touch and pen input, including pressure, tilt and buttons
- Connects automatically over USB; no IP addresses or pairing codes
- Wi-Fi connection (**beta**, opt-in) for cable-free use
- Tablet's native resolution and highest refresh rate by default, remembered per computer
- Windows and Linux computers

## Download

Get the latest [DrawSplay release](https://github.com/SolfenTheDragon/Drawsplay/releases/latest):

- **Windows:** `DrawSplay-windows-vX.Y.Z.zip` (the installer and the tablet app)
- **Linux (x86-64):** `DrawSplay-linux-vX.Y.Z.tar.gz` (the program, install/uninstall scripts and the tablet app)
- **Tablet app only:** `DrawSplay.APK`

## Requirements

- A USB cable between the computer and the tablet.
- **Windows:** Windows 10 (version 1903 or later) or Windows 11, and internet access while installing (the installer downloads ffmpeg).
- **Linux:** an x86-64 computer with ffmpeg. The extended desktop works on GNOME, KDE Plasma 6, sway and Hyprland (Wayland), and on X11 when your graphics card has an unused HDMI port. Otherwise DrawSplay mirrors your screen.
- **Tablet:** Android 8 or later. Developer options and USB debugging are not needed.

## Install

### Windows

1. Install the app on the tablet: copy `DrawSplay.APK` to it, open it and allow installs from that source if asked.
2. Extract the zip and run `drawsplay_installer_vX.Y.Z.exe`. Choose the desktop shortcut and startup preferences on one setup screen. Select **Enable an extra screen** to install the virtual display driver; leave it off for mirror-only setup without a new display driver. Setup explains any local signing-certificate change before you proceed. Click **Install**, then approve the administrator prompt once. A progress window stays visible while files, ffmpeg and USB drivers are installed.
3. DrawSplay runs in the **system tray**. Right-click the tray icon to open settings, start/stop or exit.

If the tablet isn't detected, unplug and reconnect it. If it still isn't detected, run the installer again.

For a silent install, run `drawsplay_installer_vX.Y.Z.exe -install -quiet` (add `-desktop-shortcut`, `-no-autostart` or `-no-driver-install` as wanted).

**Uninstall** from **Apps & Features** or the **Uninstall DrawSplay** Start Menu shortcut. Add `-keep-settings` when running `DrawSplay-Uninstall.exe` by hand to keep your settings.

### Linux

1. Extract the download and run the installer from the extracted folder, as your normal user (not with `sudo`):
   ```bash
   tar -xzf DrawSplay-linux-*.tar.gz
   cd DrawSplay-linux-*/
   ./install-drawsplay-host-linux.sh
   ```
   It adds DrawSplay to your menu and to start at login, installs missing tools on Ubuntu/Debian, and asks for your password to set up USB and input permissions. Log out and back in if it asks you to.
2. Install the app on the tablet: copy `DrawSplay.APK` to it, open it and allow installs from that source if asked.

For smoother streaming, install your graphics card's VA-API driver (`intel-media-va-driver-non-free` for Intel, `mesa-va-drivers` for AMD).

To remove DrawSplay, run `./uninstall-drawsplay-host-linux.sh` (your settings are kept).

## Use

1. Connect the tablet by USB. The first time, accept the prompt on the tablet.
2. Start DrawSplay on the computer (it starts at sign-in if you chose that).
3. Open DrawSplay on the tablet.

The tablet shows the extended desktop and sends touch and pen input back. It reconnects by itself after drops and unplugging. While the display is shown, swipe from a screen edge and press **Back** for **Connection options**.

The waiting screen follows the tablet's light/dark theme. **Show setup help** expands connection instructions. A once-only hint explains Back without blocking drawing; local setup screens and dialogs keep keyboard input on the tablet instead of forwarding it to the computer.

**USB file transfer:** while DrawSplay runs, it takes over the tablet's USB connection, so *File transfer* mode is not available. On Linux, pick *File transfer* on the tablet to move files, then *No data transfer* to return to DrawSplay. On Windows, quit DrawSplay to transfer files. To switch only when you choose, turn off **Switch the tablet to accessory mode automatically** in the settings and use **Switch to USB accessory mode** when you want DrawSplay.

## Settings

While DrawSplay is running, open `http://127.0.0.1:27180` (or **Settings** in the tray menu) to:

- change tablet scaling and turn touch or pen input on and off
- reset a connection, lock input for a while, or reconnect video
- see setup checks and connection health, and download a diagnostics file
- turn **Run at login** and **Open the tablet app when USB connects** on or off

The navigation groups **Overview**, **Display & input**, **Connections**, **Troubleshooting**, and **Settings & about**. Appearance can follow the system or use light/dark mode. Preference edits show a **Save changes / Discard** bar; only changed preferences are sent, and unrelated changes do not restart video. Startup and live session controls apply immediately. Tablet settings are disabled during a PC-only session or when the host cannot be reached; unsaved edits are kept on this page.

**Pen alignment:** in **Pen calibration**, choose **Calibrate with targets**, move the browser onto the tablet's display, and enter fullscreen. Tap the four targets with your pen, inspect the corrected-position preview, then choose **Use these adjustments** and **Save calibration**. Nothing is saved by entering or cancelling the guide. Manual adjustments, reset and discard are also available.

**Resolution and refresh rate:** change them in your computer's display settings (Windows Display Settings; GNOME Settings > Displays; on X11, the `HDMI-1` or `Virtual-1-1` display). On KDE Plasma use DrawSplay's settings page. Linux streams at up to 60 fps (up to 120 on X11 with Intel or AMD graphics).

## Troubleshooting

**Tablet doesn't connect.** Unplug and replug the cable and accept the prompt on the tablet. On Windows, run the installer again; on Linux, run the install script again and log out and back in.

**Linux: pen or touch does nothing.** Run the install script again, then log out and back in.

**Linux: the mouse cursor disappears.** Press Ctrl+Alt+Del and move the mouse. Turning off your desktop's "hide the pointer while typing" setting prevents it.

**Linux shows a mirror instead of an extra display.**
- Wayland: use GNOME, KDE Plasma 6, sway or Hyprland.
- X11: DrawSplay first tries the `vkms` virtual-display module, then an unused HDMI port, then mirroring. It may ask for your password when the tablet connects. Automatic `vkms` loading is enabled by default but blocked after detected setup crashes or known kernel failures. Set `DRAWSPLAY_X11_VKMS_AUTOLOAD=0` to disable loading it. Setting it to `1` **overrides the crash guards** and can crash the desktop session; do not use that as a routine repair.
- The settings page shows the reason.

**Windows: no extended display.** Run the installer again and restart Windows if it asks.

**Black screen or no video.** Keep both apps open and try a lower bitrate or resolution in the settings.

**Video lags or stutters on an older tablet.** The settings page shows a **Recommended maximum for this tablet** resolution when the tablet is connected. Set the tablet's display to that size in your computer's display settings.

**Multi-touch doesn't work in an X11 app.** Some apps need touch turned on; for Firefox start it with `MOZ_USE_XINPUT2=1 firefox`.

**The tablet doesn't open DrawSplay when plugged in.** Turn on **Open the tablet app when USB connects** in the settings and save, or open the app yourself.

## Wi-Fi (beta)

Wi-Fi works for display, touch and pen, but it is in beta: expect more delay than USB. Use USB when you can; when both are connected, USB is used.

1. In the settings (`http://127.0.0.1:27180`), turn on **Wi-Fi**, choose your network and save. A pairing code appears in **Host overview**.
2. On the tablet, tap **Wi-Fi connect** and enter the eight digits.
3. When the tablet says the code is verified, click **Approve** on the computer.

The tablet then reconnects to that computer by itself. Both must be on the same network (not a guest network). Connections are encrypted and only paired devices are accepted. For the best picture use a 5 GHz network with a strong signal; **Host overview** shows the signal strength.

**Switch to a different computer:** on the tablet's waiting screen, tap **Forget wireless host**, confirm, then pair with the new computer. If the display is showing, use **Back → Connection options** first.

**Wi-Fi doesn't connect:** unplug the USB cable (USB takes priority), and check that DrawSplay on the computer has Wi-Fi turned on with the right network chosen.

## DrawSplay Desktop (a PC as the second screen)

DrawSplay Desktop turns one computer into an extra display for another, with mouse, keyboard, touch and pen sent back. It is a separate download (`DrawSplayDesktop-…` on the release page) and does not use the tablet app.

- **`dd-host`** runs on the computer whose desktop you want to extend.
- **`dd-client`** runs on the computer that becomes the extra display.

Windows and Linux work in any combination.

### Requirements

- `ffmpeg` installed on both computers.
- A network connection between them (Wi-Fi, Ethernet or a USB network cable).
- **Windows host:** the DrawSplay virtual display driver (installed by the DrawSplay installer).
- **Linux host:** an X11 session with a spare display output, or Wayland: sway or Hyprland with `wf-recorder`, or GNOME/KDE with `gstreamer1.0-tools` and `gstreamer1.0-pipewire`.
- **Linux host, for touch, pen and the full keyboard:** run `install-linux.sh --udev` from the download once, then log out and back in.

### Start

```sh
dd-host --bind 0.0.0.0:47800              # on the computer to extend
dd-client                               # on the computer that becomes the display
```

The client launcher offers **Connect**, **Trusted devices**, and **Quit**. Enter the host's LAN address, then choose a fitted window, fitted fullscreen, or a 1:1 window. For terminal/automated use, `dd-client HOST_IP --fullscreen` still works. Linux graphical dialogs need `zenity`; unavailable dialogs fall back to terminal prompts. `--terminal` explicitly uses terminal prompts.

The host command above listens on all interfaces; prefer `--bind YOUR_LAN_IP:47800` to select one interface. Allow the chosen port through your firewall if necessary. Bare `dd-host` offers a local start/device-management menu, but still listens only on `127.0.0.1:47800`; that menu does not enable LAN access.

Press `Ctrl+Alt+Shift+Q` to close the client. `--size 1920x1080` opens a window instead of fullscreen, `--fit` scales the picture to the window, and `--help` lists all options.

### Pairing

The first time, both computers show their identities and a 10-digit code. Approve on both only if the codes match; otherwise reject. With terminal prompts, type `y` to approve or `n` to reject. Requests expire after two minutes and close when the peer disconnects. After approval, the computers recognise each other.

For deployments without graphical dialogs or an interactive terminal, pair by exchanging keys:

```
dd-client --show-key        # on the client: prints its key
dd-host --trust <that key>  # on the host
dd-host --show-key          # on the host
dd-client --trust <that key>
```

**Trusted devices** in the launcher, or `--manage-trusted` on either binary, lets you name or remove paired computers. Removal asks for confirmation. `--list-trusted` and `--forget KEY_PREFIX` remain available for terminal use.

A paired computer can type, click and draw on the host as you, so pair only computers you trust. The host listens only on loopback unless you choose another bind address; the peer-address filter still rejects public-network peers by default. If it can't create an extra display it shows your real screen instead; start it with `--require-virtual` to prevent that.

If a connection drops, the client clears the stale picture, stops forwarding input and shows reconnect progress even in fullscreen. It retries for about a minute, then offers retry, trusted-device management or quit.

### Limitations

- No audio.
- One client at a time unless the host is started with `--max-clients N`.
- Pen pressure, tilt and the pen's buttons work only when the client runs on Windows. On a Linux client the pen acts as a mouse.
- Display mode choices are made at startup; there is no live drawing toolbar.

## Privacy

DrawSplay's settings page and connections stay on your computer, except Wi-Fi mode, which connects to your paired tablet over encrypted connections on your local network. See [PRIVACY.md](PRIVACY.md).

## Security

Report security problems privately as described in [SECURITY.md](SECURITY.md).

## Building from source

See [BUILD.md](BUILD.md).

## License

DrawSplay is licensed under the GNU General Public License v3.0. See [LICENSE](LICENSE).

DrawSplay bundles a Windows virtual display driver derived from Microsoft's IddCx sample under the Microsoft Public License (MS-PL); see [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
