# DrawSplay

Use your Android tablet as a touch- and pen-enabled extension of your computer's desktop. DrawSplay streams your desktop to the tablet and sends touch and stylus input back to the computer.

## Features

- Extended desktop on the tablet (mirroring when a virtual display isn't available)
- Touch, multi-touch and pen input, including pressure, tilt and buttons
- Connects automatically over USB; no IP addresses or pairing codes
- Tablet's native resolution and highest refresh rate by default (60 fps on Linux), remembered per computer
- Local settings page for bitrate, input toggles and diagnostics
- Windows and Linux hosts

## Download

Get the latest [DrawSplay release](https://github.com/SolfenTheDragon/Drawsplay/releases/latest):

- **Windows:** `DrawSplay-windows-vX.Y.Z.zip` (a folder with the installer and the APK)
- **Linux (x86-64):** `DrawSplay-linux-vX.Y.Z.tar.gz` (a folder with the host, install/uninstall scripts, icon, udev rule and the APK)
- **Android app:** `DrawSplay.APK` (also inside both folders)

No source build is required.

## Requirements

- A USB cable between the computer and the tablet
- **Windows:** Windows 10/11 (the extended display needs Windows 10 version 1903 or later; any NVIDIA, AMD or Intel GPU works), with the WinUSB driver bound to the tablet (see Install). Internet access during install: the installer downloads the newest ffmpeg build compatible with your Windows version and CPU (SHA-256 verified). adb is not needed or downloaded.
- **Linux:** an x86-64 computer with ffmpeg. Wayland is preferred (GNOME, KDE Plasma 6, sway and Hyprland support an extended desktop); X11 also works and needs the `vkms` kernel module for an extended desktop. Without a virtual display DrawSplay mirrors your screen instead. On X11 with an Intel GPU an optional native helper (`drawsplay-media`, see `BUILD.md`) lowers latency; ffmpeg remains the automatic fallback, and AMD/NVIDIA keep using ffmpeg unless you opt in.
- **Tablet:** an Android device. No Developer options or USB debugging are needed.

## Install

### Windows

1. Install the app on the tablet: copy `DrawSplay.APK` to it, open it and allow installs from that source if asked.
2. Extract `DrawSplay-windows-vX.Y.Z.zip` and run `drawsplay_installer_vX.Y.Z.exe`. It asks whether to add a desktop shortcut and whether to start DrawSplay automatically when you sign in, then Windows asks for administrator approval **once**; setup then:
   - installs DrawSplay to `C:\Program Files\DrawSplay\` (with `DrawSplay-Uninstall.exe`),
   - installs DrawSplay's bundled virtual display driver and `ffmpeg`,
   - installs a WinUSB driver for the tablet (signed on your PC, so Zadig is not needed) and a helper so newly attached tablets get it without another UAC prompt,
   - stores settings, logs and install state in `%APPDATA%\DrawSplay\`, adds Start Menu shortcuts (and a desktop shortcut if you choose it) and, if you choose it, starts DrawSplay at sign-in (changeable later on the settings page).

   For an unattended install run `drawsplay_installer_vX.Y.Z.exe -install -quiet`, adding `-desktop-shortcut` and/or `-no-autostart` (or `-autostart`) as wanted; a quiet reinstall keeps the existing shortcut and sign-in setting.
3. DrawSplay runs in the **system tray** (no console window). Right-click the tray icon to open settings, start/stop, or exit.
4. If the tablet isn't detected, unplug and reconnect it; DrawSplay binds its WinUSB driver to the tablet automatically (normal mode first, then accessory mode). If it still isn't detected, rerun the installer. This replaces the tablet's file-transfer/ADB USB driver while DrawSplay's driver is bound; uninstalling DrawSplay restores it.

To uninstall, use **Apps & Features**, the **Uninstall DrawSplay** Start Menu shortcut, or `C:\Program Files\DrawSplay\DrawSplay-Uninstall.exe` (add `-quiet` for no dialogs, `-keep-settings` to keep `%APPDATA%\DrawSplay`). The uninstaller also removes DrawSplay's dependencies: the bundled ffmpeg, WinUSB drivers DrawSplay installed (and their signing certificate), and DrawSplay's own virtual display driver. Components that other software may also use (a legacy Parsec driver when the Parsec app is installed, Zadig WinUSB bindings) are removed only if you confirm (`-remove-shared` with `-quiet`).

To run DrawSplay without installing (portable), rename the downloaded file to `DrawSplay.exe`, run it and choose **No** when it offers to install; it then asks for administrator approval only when a driver is first needed, and never twice for the same USB device. For a console with live logs, run `DrawSplay.exe -background=false` from a terminal.

### Linux

1. Download and extract `DrawSplay-linux-vX.Y.Z.tar.gz` (`tar -xzf DrawSplay-linux-*.tar.gz`), open the extracted folder, then run:
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

**Resolution and refresh rate.** The first time a tablet connects to a computer it uses the tablet's native resolution at its highest refresh rate (60 fps on Linux hosts). Changes you make are remembered by the tablet, separately for each computer.

- Windows: use **Windows Display Settings**. The tablet's native resolution is offered at its panel's highest refresh rate (for example 120 Hz), and DrawSplay never streams faster than the tablet's screen can show.
- GNOME Wayland: **Settings > Displays**.
- X11: your desktop's display settings (the `Virtual-1-1` display).
- sway / Hyprland: their output commands.
- KDE Plasma: DrawSplay's settings page.

On Linux the frame rate is limited to 60, except on X11 hosts with VA-API (Intel/AMD) where the tablet may request up to 120. The settings page can turn that 120 fps capture off, and can pause X11 capture while the screen is idle (saves CPU and bandwidth; off by default). Some compositors, such as Muffin, report damage for the whole X screen, so any animation on any monitor keeps capture running.

## Troubleshooting

**Tablet doesn't connect over USB.** Check the Linux udev rule or the Windows WinUSB driver (see Install), replug the cable and accept the USB prompt on the tablet.

**Linux: `no permissions` or pen/touch not working.** Check that the Android USB rules are installed and you're in `plugdev`; for input, check the `/dev/uinput` rule and `input` group. Log out and back in afterwards.

**Linux: the mouse cursor disappears after starting DrawSplay.** Press Ctrl+Alt+Del to open the session menu and move the mouse to bring the cursor back. This comes from the desktop's "hide the pointer while typing" setting reacting to DrawSplay's virtual keyboard and pen devices; turning that setting off prevents it.

**Linux shows a mirror instead of an extra display.**
- Wayland: use GNOME, KDE Plasma 6, sway or Hyprland. Other Wayland desktops can't extend and show only X11 apps.
- X11: run `sudo modprobe vkms`, then reconnect; log out and back in if `xrandr` doesn't list `Virtual-1-1`.
- The host log states the reason, and the settings page shows warnings.

**Multi-touch gestures don't work in an X11 app.** Some apps need touch enabled; for Firefox start it with `MOZ_USE_XINPUT2=1 firefox`.

**Black screen or no video.** Check `ffmpeg -version` (or `"C:\Program Files\DrawSplay\runtime\ffmpeg\ffmpeg.exe" -version`), keep the host and tablet app open, and try a lower bitrate or resolution on the settings page.

**Windows: no extended display.** Run the installer (or `"C:\Program Files\DrawSplay\DrawSplay.exe" -install`) again to repair the driver setup (one UAC prompt); reboot if Windows asks. If custom resolutions are missing, run **Enable custom-resolution registry access (Admin)** once on the settings page, then reconnect.

**Tablet shows no prompt / doesn't open when plugged in.** Turn on **Auto-launch Android app on USB connect** on the settings page, or open the app manually.

## Privacy

The host listens only on your computer (localhost) and the settings page is local-only. See [PRIVACY.md](PRIVACY.md) for the full privacy policy.

## License

DrawSplay is licensed under the GNU General Public License v3.0. See [LICENSE](LICENSE).

DrawSplay bundles a Windows virtual display driver derived from Microsoft's IddCx sample under the Microsoft Public License (MS-PL); see [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
