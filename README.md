# DrawSplay

Use your Android tablet as a touch- and pen-enabled extension of your computer's desktop. DrawSplay streams the desktop to the tablet and sends touch and stylus input back to the computer.

Download the latest [DrawSplay release](https://github.com/SolfenTheDragon/Drawsplay/releases/latest). This guide covers installing and using its packaged app and host:

- **Windows host:** `DrawSplay.exe`
- **Linux host (x86-64/amd64):** `drawsplay-host-linux-amd64` and `launch-drawsplay-host.sh`; optional `install-drawsplay-host-linux.sh` and `uninstall-drawsplay-host-linux.sh` manage desktop and applications-menu launchers
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

### Linux host PC

Use the Linux amd64 download on an x86-64 computer. **Wayland is the preferred Linux session**, with X11 as the backup for desktops without a supported Wayland implementation. **Ubuntu 22.04+ with GNOME Wayland** supports an extended desktop on the tablet (see [GNOME Wayland extended display](#gnome-wayland-extended-display)), as do **KDE Plasma 6, sway and Hyprland** (see [KDE Plasma, sway and Hyprland](#kde-plasma-sway-and-hyprland)). **X11 sessions, including Ubuntu Cinnamon/Linux Mint Cinnamon, MATE and Xfce, are the backup: they support an extended desktop once the `vkms` kernel module is loaded** (see [X11 extended display](#x11-extended-display-vkms)); without it they mirror the existing desktop. Other Wayland desktops, such as Cinnamon on Wayland, cannot extend the desktop: the tablet mirrors XWayland's screen, which shows only X11 applications (see [Other Wayland desktops](#other-wayland-desktops)).

Linux needs ADB and ffmpeg. GStreamer with its PipeWire plugin is needed for the GNOME and KDE Plasma Wayland extended display. The X11 backup also needs access to the active X11 display, plus `xrandr` for its extended display. Touch and pen input require access to `/dev/uinput`. See the Linux setup below.

---

## Install the host and app

### Prepare Windows files

Place the binaries somewhere easy to find, for example:

`C:\DrawSplay\`

Expected files:

- `C:\DrawSplay\DrawSplay.exe`
- `C:\DrawSplay\DrawSplay.APK`

Optional prebundled runtime files (recommended for offline setup):

- `C:\DrawSplay\runtime\adb\adb.exe`
- `C:\DrawSplay\runtime\ffmpeg\ffmpeg.exe` (plus ffmpeg DLLs)

---

### Install the Android app on Windows

Connect the tablet over USB, then run:

```powershell
adb devices
adb install -r C:\DrawSplay\DrawSplay.APK
```

If prompted on the tablet, allow USB debugging for this computer.

---

### Start the Windows host

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

### Install and start on Linux

Download `drawsplay-host-linux-amd64`, `launch-drawsplay-host.sh`, and `DrawSplay.APK` into the same folder. Open a terminal in that folder:

```bash
chmod +x drawsplay-host-linux-amd64 launch-drawsplay-host.sh
./launch-drawsplay-host.sh
```

Run the launcher as your normal desktop user, not with `sudo`. On Ubuntu/Debian, it attempts to install missing tools and set up USB/input permissions, asking for your sudo password when needed. These changes can require logging out and back in or restarting. To keep system changes manual, run `./launch-drawsplay-host.sh --no-install-deps` instead.

To add DrawSplay to the applications menu (including GNOME's app grid) and create a desktop shortcut, download `install-drawsplay-host-linux.sh` and `drawsplay-tray.png` into the same folder as the host binary, then run:

```bash
chmod +x install-drawsplay-host-linux.sh
./install-drawsplay-host-linux.sh
```

The installer copies the host into `~/.local/bin`, installs the launchers and icon under `~/.local/share`, and enables starting the host at login. On GNOME, if the desktop shortcut is not immediately launchable, use its context menu to allow launching.

To remove the installed host and launchers while keeping DrawSplay settings and logs, run:

```bash
./uninstall-drawsplay-host-linux.sh
```

For manual Ubuntu setup:

```bash
sudo apt install android-sdk-platform-tools android-sdk-platform-tools-common ffmpeg
echo 'KERNEL=="uinput", GROUP="input", MODE="0660"' | sudo tee /etc/udev/rules.d/99-drawsplay-uinput.rules
sudo usermod -aG input,plugdev "$USER"
```

For the GNOME or KDE Plasma Wayland extended display, also install the PipeWire capture packages:

```bash
sudo apt install gstreamer1.0-tools gstreamer1.0-plugins-good gstreamer1.0-plugins-bad \
  gstreamer1.0-plugins-ugly gstreamer1.0-pipewire
```

For the X11 backup (Cinnamon, MATE, Xfce), install `xrandr` instead (`sudo apt install x11-xserver-utils`) and see [X11 extended display](#x11-extended-display-vkms).

Restart after setting up permissions. On other distributions, install the equivalent packages using your package manager; the launcher's automatic package installation requires `apt-get`.

Connect the tablet by USB, accept its USB debugging prompt, then install the app from a second terminal in the download folder:

```bash
adb devices
adb install -r ./DrawSplay.APK
```

Keep the host running and open DrawSplay on the tablet. DrawSplay maps the pen and touch input to the tablet's display itself.

For smoother streaming, install your GPU's VA-API encoding driver. On Ubuntu, use `intel-media-va-driver-non-free` for supported Intel graphics or `mesa-va-drivers` for AMD, plus `vainfo`. Without hardware encoding, software encoding can reduce performance.

#### X11 extended display (vkms)

X11 is the backup implementation, for desktops without a supported Wayland one. X11 desktops cannot create virtual monitors, so DrawSplay extends the desktop onto a virtual display output provided by the `vkms` kernel module, included in Ubuntu's kernels:

```bash
sudo modprobe vkms
```

The host also loads vkms by itself when it starts and whenever a tablet is connected, if the X11 session has no virtual output yet. If that needs a password, a prompt appears (a polkit dialog, or a zenity/kdialog password box); dismiss it to keep mirroring, and DrawSplay asks again no sooner than two minutes later, on the next connection. Boot-time loading is not changed by this. The launcher offers to load the module and configure it to load automatically at boot. If vkms was loaded manually, the launcher can also configure boot-time loading on a later run. Save your work first: loading it during an X11 session reconfigures the X server's displays. To configure boot-time loading manually, run `echo vkms | sudo tee /etc/modules-load.d/drawsplay-vkms.conf`.

Once the module is loaded, every tablet connection switches the virtual display (`Virtual-1-1`) on at the configured resolution, to the right of your other monitors, and DrawSplay streams only that display. If the virtual output appears after a session has already started in mirror mode, DrawSplay detects it and restarts the stream in extended mode automatically. Arrange it or change its resolution in your desktop's display settings (Cinnamon: **System Settings > Display**); DrawSplay follows the change and remembers the resolution. Touch and pen input are mapped to the tablet's display automatically: through Cinnamon's or GNOME's tablet settings when one of those desktops runs, otherwise by DrawSplay itself. While your computer is locked, the tablet stream switches to black and resumes after you unlock it. The virtual display is switched off a few seconds after the tablet disconnects and when the host exits; if you had made it your primary or only display, your monitor is made primary (and switched back on) instead. To use a different output, such as a DisplayLink adapter's, set `DRAWSPLAY_X11_OUTPUT` to its name in `xrandr` before starting the host.

#### GNOME Wayland extended display

GNOME creates the tablet's display itself, so no kernel module is needed; install the PipeWire capture packages above. It works like the [X11 extended display](#x11-extended-display-vkms): every tablet connection adds a virtual monitor at the configured resolution, to the right of your other monitors, and DrawSplay streams only that monitor. GNOME would arrange all your displays again in a row for the new monitor; DrawSplay keeps them as you arranged them, including their scale, primary display and settings such as underscanning. Arrange the virtual monitor or change its resolution in **Settings > Displays** (it offers the standard resolutions and the tablet's native one); DrawSplay follows the change, remembers the resolution and, on the next connection, puts the monitor back where you last had it. Touch and pen input are mapped to the tablet's display automatically, through GNOME's tablet settings. While your computer is locked, the tablet stream switches to black and resumes after you unlock it. The virtual monitor is removed a few seconds after the tablet disconnects and when the host exits, and your other displays are left as you arranged them. DrawSplay never saves a display configuration, so the arrangement saved in Settings is unchanged. The status icon appears in the system tray under Wayland too; stock GNOME needs the AppIndicator extension for it (Ubuntu includes it).

Differences from the X11 extended display:

- Don't switch the virtual monitor off in **Settings > Displays**: GNOME Shell 50 crashes on that. Disconnect the tablet instead.
- Stopping the screen share from GNOME's top-bar indicator removes the virtual monitor; DrawSplay adds a new one in its place.
- Rotating the virtual monitor is not supported: GNOME streams it unrotated.
- Moving the virtual monitor doesn't interrupt the stream.

#### KDE Plasma, sway and Hyprland

These desktops create the tablet's display themselves, so no kernel module is needed. Like the [X11 extended display](#x11-extended-display-vkms), every tablet connection gets a virtual display at the configured resolution, to the right of your other monitors (or where you last had it), and DrawSplay streams only that display. Touch and pen input are mapped to it automatically. DrawSplay follows your changes and remembers the resolution. A reconnect within a few seconds keeps the display. It is removed a few seconds after the tablet disconnects and when the host exits.

- **KDE Plasma 6** (KWin): install the PipeWire capture packages above. On first use the host installs `~/.local/share/applications/drawsplay-host.desktop`, which lets it use KWin's screencast protocol; if the display does not appear the first time, reconnect the tablet. KWin lays out the `Virtual-Drawsplay` display (place, scale and rotation) as you last arranged it in **System Settings > Display & Monitor**. Its resolution is fixed while it exists, so change it in DrawSplay's settings panel.
- **sway** (1.9 or newer removes the display completely; older versions switch it off): arrange it with `swaymsg output HEADLESS-N ...` or a tool such as wdisplays. Like X11, the other displays move so the desktop's top-left corner stays at 0,0.
- **Hyprland**: the display is named `Drawsplay`; arrange it with `hyprctl keyword monitor Drawsplay,...` (or `hl.monitor` with a Lua configuration). Your other monitors' rules are left alone.

DrawSplay sets the display up at runtime, so a configuration reload (`swaymsg reload`, saving `hyprland.conf`) resets it to your config's rules; DrawSplay then restarts the stream without saving that resolution. Rules for the virtual display in your config files are overridden on each connection.

#### Other Wayland desktops

On other Wayland desktops, such as Cinnamon on Wayland (still experimental), DrawSplay cannot create a virtual display: the tablet mirrors XWayland's screen instead, so only X11 applications appear (native Wayland windows are black), and the desktop is not extended. The settings panel shows a warning and a **Wayland virtual display** checklist item. For an extended display, log into an X11 session (such as Cinnamon on X11) and load `vkms`, or use GNOME, KDE Plasma, sway or Hyprland.

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

On **GNOME Wayland**, change the tablet's resolution in **Settings > Displays**; on an **X11 extended display**, in your desktop's display settings (the `Virtual-1-1` display); on **sway** and **Hyprland**, with their output commands; on **KDE Plasma**, in DrawSplay's settings panel. Frame rate remains configurable in DrawSplay's settings panel.

---

## Daily use

1. Connect tablet over USB
2. Start `DrawSplay.exe` on Windows or `./launch-drawsplay-host.sh` on Linux
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

### Linux USB or pen input does not work

If `adb devices` reports `no permissions`, check that Android USB rules are installed and your user belongs to `plugdev`. If the desktop appears but touch or pen does not work, check the `/dev/uinput` rule and `input` group membership. Log out and back in after changing groups; restart after adding the input rule if permissions are still incorrect.

Occasionally Linux may show two cursors after switching between mouse and pen input; this is a known GNOME/Mutter limitation. On an extended display (GNOME Wayland or X11), the host log reports where the pen was mapped (`Mapped Drawsplay Pen ...`); if touch lands on another monitor, check that the tablet's **Map to Monitor** setting in Cinnamon's or GNOME's tablet settings points at the virtual display.

### Multi-touch gestures do not work in an app on X11

DrawSplay delivers real multi-touch (up to 10 fingers) to X11 sessions, but some applications ignore it unless it is enabled for them. Firefox, for example, only handles touch (two-finger scroll, pinch) when started with `MOZ_USE_XINPUT2=1`: quit it completely, then run `MOZ_USE_XINPUT2=1 firefox`. To make that permanent, copy the app's `.desktop` file to `~/.local/share/applications/` and prefix its `Exec=` lines with `env MOZ_USE_XINPUT2=1`. Other applications do not support touch gestures at all (GIMP has no canvas pinch or two-finger gestures); Krita, Xournal++ and Inkscape do. Wayland sessions are unaffected.

### Linux shows a mirror instead of an extra display

On Wayland, extended display requires GNOME, KDE Plasma 6, sway or Hyprland (GNOME and KDE Plasma also need working GStreamer/PipeWire tools); on other Wayland desktops, the tablet mirrors XWayland's screen, which shows only X11 applications (see [Other Wayland desktops](#other-wayland-desktops)). The host log explains why GNOME's virtual monitor was not used (`PipeWire capture tooling unavailable ...` or `Mutter virtual monitor unavailable ...`). On X11, load the `vkms` kernel module (`sudo modprobe vkms`) and reconnect the tablet; see [X11 extended display](#x11-extended-display-vkms). If the module is loaded but `xrandr` lists no `Virtual-1-1` output, log out and back in. The host log explains why it fell back to mirroring (`X11 extended display unavailable ...`). Check the warnings in DrawSplay's settings panel.

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
