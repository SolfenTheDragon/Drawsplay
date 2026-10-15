# DrawSplay

Use your Android tablet as a touch- and pen-enabled extension of your computer's desktop. DrawSplay streams your desktop to the tablet and sends touch and stylus input back to the computer.

## Features

- Extended desktop on the tablet (mirroring when a virtual display isn't available)
- Touch, multi-touch and pen input, including pressure, tilt and buttons
- Connects automatically over USB; no IP addresses or pairing codes
- Tablet's native resolution and 60 fps by default, remembered per computer
- Local settings page for bitrate, input toggles and diagnostics
- Windows and Linux hosts

## Download

Get the latest [DrawSplay release](https://github.com/SolfenTheDragon/Drawsplay/releases/latest):

- **Windows host:** `DrawSplay.exe`
- **Linux host (x86-64):** `drawsplay-host-linux-amd64` and `install-drawsplay-host-linux.sh` (required); optional `uninstall-drawsplay-host-linux.sh`
- **Android app:** `DrawSplay.APK`

No source build is required.

## Requirements

- A USB cable between the computer and the tablet
- **Windows:** Windows 10/11, with the WinUSB driver bound to the tablet (see Install). Internet access on first run if `ffmpeg` isn't already present or bundled.
- **Linux:** an x86-64 computer with ffmpeg. Wayland is preferred (GNOME, KDE Plasma 6, sway and Hyprland support an extended desktop); X11 also works and needs the `vkms` kernel module for an extended desktop. Without a virtual display DrawSplay mirrors your screen instead.
- **Tablet:** an Android device. No Developer options or USB debugging are needed.

## Install

### Windows

1. Put `DrawSplay.exe` and `DrawSplay.APK` in a folder such as `C:\DrawSplay\`.
2. Install the app on the tablet: copy `DrawSplay.APK` to it, open it and allow installs from that source if asked.
3. Bind the WinUSB driver to the tablet so the host can open it over USB; see `packaging/windows/README.md` (this path hasn't been tested on hardware yet).
4. Start the host:
   ```powershell
   cd C:\DrawSplay
   .\DrawSplay.exe
   ```

On first run the host sets up `ffmpeg` automatically if needed and may install a virtual display driver and, when you plug in the tablet, the WinUSB driver for it (approve the UAC prompts).

### Linux

1. Download `drawsplay-host-linux-amd64`, `install-drawsplay-host-linux.sh`, `drawsplay-tray.png`, `70-drawsplay-aoa.rules` and `DrawSplay.APK` into one folder, then run:
   ```bash
   chmod +x install-drawsplay-host-linux.sh
   ./install-drawsplay-host-linux.sh
   ```
   Run it as your normal user, not with `sudo`. It installs the host, menu and desktop launchers and start at login; on Ubuntu/Debian it also installs missing tools and sets up USB/input permissions (asking for your password). Log out and back in or restart afterwards if asked. Use `--no-install-deps` to keep system changes manual.
2. Install the app on the tablet: copy `DrawSplay.APK` to it, open it and allow installs from that source if asked.
3. Remove the host with `./uninstall-drawsplay-host-linux.sh` (settings and logs are kept).

For smoother streaming, install your GPU's VA-API driver (for example `intel-media-va-driver-non-free` or `mesa-va-drivers`).

More per-desktop detail (X11/vkms, GNOME, KDE, sway, Hyprland) is in the project wiki.

## Use

1. Connect the tablet by USB.
2. Start the host (`DrawSplay.exe` on Windows, the DrawSplay menu entry or desktop shortcut on Linux).
3. Open DrawSplay on the tablet.

The tablet connects on its own, shows the extended desktop and sends touch and pen input back. It reconnects after drops and unplugging.

## Connection mode

DrawSplay connects to the tablet over USB. The first time, accept the prompt on the tablet.

## Configuration

While the host is running, open `http://127.0.0.1:27180` to:

- change bitrate and toggle touch/pen input
- set the `ffmpeg` path
- disconnect the tablet, lock input temporarily, or reconnect video
- see the first-run checklist and live health counters
- download a diagnostics snapshot
- toggle **Run at login**
- toggle **Auto-launch Android app on USB connect**

Video setting changes restart the stream automatically.

**Resolution and refresh rate.** The first time a tablet connects to a computer it uses the tablet's native resolution at 60 fps. Changes you make are remembered by the tablet, separately for each computer.

- Windows: use **Windows Display Settings**.
- GNOME Wayland: **Settings > Displays**.
- X11: your desktop's display settings (the `Virtual-1-1` display).
- sway / Hyprland: their output commands.
- KDE Plasma: DrawSplay's settings page.

On Linux the frame rate is limited to 60.

## Troubleshooting

**Tablet doesn't connect over USB.** Check the Linux udev rule or the Windows WinUSB driver (see Install), replug the cable and accept the USB prompt on the tablet.

**Linux: `no permissions` or pen/touch not working.** Check that the Android USB rules are installed and you're in `plugdev`; for input, check the `/dev/uinput` rule and `input` group. Log out and back in afterwards.

**Linux shows a mirror instead of an extra display.**
- Wayland: use GNOME, KDE Plasma 6, sway or Hyprland. Other Wayland desktops can't extend and show only X11 apps.
- X11: run `sudo modprobe vkms`, then reconnect; log out and back in if `xrandr` doesn't list `Virtual-1-1`.
- The host log states the reason, and the settings page shows warnings.

**Multi-touch gestures don't work in an X11 app.** Some apps need touch enabled; for Firefox start it with `MOZ_USE_XINPUT2=1 firefox`.

**Black screen or no video.** Check `ffmpeg -version` (or `C:\DrawSplay\runtime\ffmpeg\ffmpeg.exe -version`), keep the host and tablet app open, and try a lower bitrate or resolution on the settings page.

**Windows: no extended display.** Restart `DrawSplay.exe` as a normal user and approve the UAC prompt; reboot if Windows asks. If custom resolutions are missing, run **Enable custom-resolution registry access (Admin)** once on the settings page, then reconnect.

**Tablet shows no prompt / doesn't open when plugged in.** Turn on **Auto-launch Android app on USB connect** on the settings page, or open the app manually.

## Privacy

The host listens only on your computer (localhost) and the settings page is local-only. See [PRIVACY.md](PRIVACY.md) for the full privacy policy.

## License

DrawSplay is licensed under the GNU General Public License v3.0. See [LICENSE](LICENSE).
