# DrawSplay

Use your Android tablet as a touch- and pen-enabled extension of your computer's desktop. DrawSplay streams your desktop to the tablet and sends touch and stylus input back to the computer.

## Features

- Extended desktop on the tablet (mirroring when a virtual display isn't available)
- Touch, multi-touch and pen input, including pressure, tilt and buttons
- Connects automatically over USB; no IP addresses or pairing codes
- Wi-Fi connection (**beta**, opt-in) for cable-free use; USB remains the preferred connection
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
- **Linux:** an x86-64 computer with ffmpeg. Wayland is preferred (GNOME, KDE Plasma 6, sway and Hyprland support an extended desktop); X11 also works: for an extended desktop it uses an unplugged HDMI port of your GPU as the tablet's display (forced on while the tablet is connected, with one password prompt), or the `vkms` kernel module (tried first; skipped automatically on kernels where it fails). Without a virtual display DrawSplay mirrors your screen instead. On X11 with an Intel GPU an optional native helper (`drawsplay-media`, see `BUILD.md`) lowers latency; ffmpeg remains the automatic fallback, and AMD/NVIDIA keep using ffmpeg unless you opt in.
- **Tablet:** an Android device. No Developer options or USB debugging are needed.

## Install

### Windows

1. Install the app on the tablet: copy `DrawSplay.APK` to it, open it and allow installs from that source if asked.
2. Extract `DrawSplay-windows-vX.Y.Z.zip` and run `drawsplay_installer_vX.Y.Z.exe`. It asks about a desktop shortcut, sign-in startup, and installing the virtual display (including local certificate trust), then Windows asks for administrator approval **once**; setup then:
   - installs DrawSplay to `C:\Program Files\DrawSplay\` (with `DrawSplay-Uninstall.exe`),
   - installs DrawSplay's bundled virtual display driver and `ffmpeg`,
   - installs a WinUSB driver for the tablet (signed on your PC, so Zadig is not needed) and a helper so newly attached tablets get it without another UAC prompt,
   - stores settings, logs and install state in `%APPDATA%\DrawSplay\`, adds Start Menu shortcuts (and a desktop shortcut if you choose it) and, if you choose it, starts DrawSplay at sign-in (changeable later on the settings page).

   If the virtual-display catalog is unsigned, setup signs it on your PC with a unique certificate, trusts its public certificate in the machine's Trusted Root and Trusted Publishers stores, and deletes the private signing key immediately. An already trusted catalog is preserved. Secure Boot and signature enforcement are not changed; managed security policies may still block installation.

   Driver signing and fallback downloads use temporary, administrator-only
   workspaces under Program Files, never your writable temporary folder.
   Setup refuses signing if those permissions cannot be verified.

   **Parsec is a fallback, not a second required driver.** If DrawSplay's driver works, setup does not download or install Parsec. Only if DrawSplay's driver fails to become available does setup reuse an existing Parsec VDD or download and install the verified Parsec VDD 0.41 fallback. Disabling automatic tool installation prevents that download. If neither driver works, DrawSplay mirrors your screen. The fallback offers only the modes already supported by Parsec; your tablet's exact native size/refresh is not guaranteed.

   For an unattended install run `drawsplay_installer_vX.Y.Z.exe -install -quiet`, adding `-desktop-shortcut` and/or `-no-autostart` (or `-autostart`) as wanted; a quiet reinstall keeps the existing shortcut and sign-in setting. Quiet installation also authorizes local virtual-display signing/trust; add `-no-driver-install` to skip it and use mirror mode.
3. DrawSplay runs in the **system tray** (no console window). Right-click the tray icon to open settings, start/stop, or exit.
4. If the tablet isn't detected, unplug and reconnect it; DrawSplay binds its WinUSB driver to the tablet automatically (normal mode first, then accessory mode). If it still isn't detected, rerun the installer. This replaces the tablet's file-transfer/ADB USB driver while DrawSplay's driver is bound; uninstalling DrawSplay restores it.

To uninstall, use **Apps & Features**, the **Uninstall DrawSplay** Start Menu shortcut, or `C:\Program Files\DrawSplay\DrawSplay-Uninstall.exe` (add `-quiet` for no dialogs, `-keep-settings` to keep `%APPDATA%\DrawSplay`). The uninstaller also removes DrawSplay's dependencies: the bundled ffmpeg, WinUSB drivers DrawSplay installed (and their signing certificate), and DrawSplay's own virtual display driver and local signing certificates. If a virtual-display package cannot be removed, its certificates are retained with a warning until driver removal succeeds. A newly installed Parsec fallback is removed automatically only when the Parsec app is absent. Pre-existing Parsec drivers, legacy drivers with ambiguous ownership, drivers shared with the Parsec app, and Zadig WinUSB bindings are removed only if you confirm (`-remove-shared` with `-quiet`).

To run DrawSplay without installing (portable), rename the downloaded file to `DrawSplay.exe`, run it and choose **No** when it offers to install; it then asks for administrator approval only when a driver is first needed, and never twice for the same USB device. For a console with live logs, run `DrawSplay.exe -background=false` from a terminal.

**USB mode note:** while DrawSplay runs it switches the tablet into USB accessory mode, which replaces the tablet's *File transfer (MTP)* mode (Android shows the "allow DrawSplay" prompt and the mode reverts). On Linux, DrawSplay leaves a tablet alone while it is in File transfer/PTP mode, so choose that mode to move files, then pick *No data transfer* to resume DrawSplay. On Windows, quit DrawSplay (or uninstall it) to use file transfer.

If you want to keep the tablet in File transfer mode until you choose otherwise, turn off **Switch the tablet to accessory mode automatically** in the settings page (`-aoa-auto-switch=false`) and press **Switch to USB accessory mode** in the host overview when you want to use DrawSplay.

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

More per-desktop detail (X11, GNOME, KDE, sway, Hyprland) is in the project wiki.

## Use

1. Connect the tablet by USB.
2. Start the host (`DrawSplay.exe` on Windows, the DrawSplay menu entry or desktop shortcut on Linux).
3. Open DrawSplay on the tablet.

The tablet connects on its own, shows the extended desktop and sends touch and pen input back. It reconnects after drops and unplugging.

While a display is connected, swipe from a screen edge to reveal Android
navigation and use **Back** to open **Connection options**. You can return to
the display or manage wireless pairing without a permanent drawing toolbar.
The setup screens keep Android navigation visible.

## Connection mode

DrawSplay connects to the tablet over USB. The first time, accept the prompt on the tablet.

**USB is the preferred connection**: it has the lowest latency and the most consistent frame rate. Wi-Fi works but is in beta (see [Wi-Fi (beta)](#wi-fi-beta)). When both are available, USB always takes priority.

### Developer USB measurements

The external [USB resource collector](tools/usb_baseline/README.md) records
read-only Linux/ADB observations without replacing the running host or app.
It is preparation for Wi-Fi performance validation, **not** a complete
latency/FPS/power benchmark or proof that USB is unaffected.

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
- X11: your desktop's display settings (the `HDMI-1` or `Virtual-1-1` display).
- sway / Hyprland: their output commands.
- KDE Plasma: DrawSplay's settings page.

On Linux the frame rate is limited to 60, except on X11 hosts with VA-API (Intel/AMD) where the tablet may request up to 120. The settings page can turn that 120 fps capture off, and can pause X11 capture while the screen is idle (saves CPU and bandwidth; off by default). Some compositors, such as Muffin, report damage for the whole X screen, so any animation on any monitor keeps capture running.

## Troubleshooting

**Tablet doesn't connect over USB.** Check the Linux udev rule or the Windows WinUSB driver (see Install), replug the cable and accept the USB prompt on the tablet.

**Linux: `no permissions` or pen/touch not working.** Check that the Android USB rules are installed and you're in `plugdev`; for input, check the `/dev/uinput` rule and `input` group. Log out and back in afterwards.

**Linux: the mouse cursor disappears after starting DrawSplay.** Press Ctrl+Alt+Del to open the session menu and move the mouse to bring the cursor back. This comes from the desktop's "hide the pointer while typing" setting reacting to DrawSplay's virtual keyboard and pen devices; turning that setting off prevents it.

**Linux shows a mirror instead of an extra display.**
- Wayland: use GNOME, KDE Plasma 6, sway or Hyprland. Other Wayland desktops can't extend and show only X11 apps.
- X11: the GPU needs an unplugged HDMI port (`ls /sys/class/drm | grep HDMI`); DrawSplay forces it on when the tablet connects and asks for your password once (`pkexec`), or run the host with passwordless `sudo`. Without a spare HDMI port, set `DRAWSPLAY_X11_VKMS_AUTOLOAD=1` to let DrawSplay load `vkms`, or load it yourself with `sudo modprobe vkms`. vkms can crash the X session on some kernels (seen on Linux 7.0 OEM with the `modesetting` driver), which is why DrawSplay guards it and falls back to the HDMI port. The installer adds a udev rule (`70-drawsplay-drm-connector.rules`) so members of `plugdev` can force the HDMI port without a password; without it you get one prompt per boot. `DRAWSPLAY_X11_VKMS_AUTOLOAD=0` turns vkms off, `=1` overrides the guard. If the host or X server dies while a virtual display is being set up, DrawSplay remembers it (`display-guard.json` in its config folder), stops using that method for a week and falls back to the next one, or to mirroring. It also ignores vkms outputs once the kernel log shows the failure. When the tablet disconnects the display is switched off, but the port stays forced until you reboot or run `echo detect | sudo tee /sys/class/drm/card0-HDMI-A-1/status` (use your card and port); nothing is kept across reboots.
- The host log states the reason, and the settings page shows warnings.

**Multi-touch gestures don't work in an X11 app.** Some apps need touch enabled; for Firefox start it with `MOZ_USE_XINPUT2=1 firefox`.

**Black screen or no video.** Check `ffmpeg -version` (or `"C:\Program Files\DrawSplay\runtime\ffmpeg\ffmpeg.exe" -version`), keep the host and tablet app open, and try a lower bitrate or resolution on the settings page.

**Windows: no extended display.** Run the installer (or `"C:\Program Files\DrawSplay\DrawSplay.exe" -install`) again to repair the driver setup (one UAC prompt); reboot if Windows asks. DrawSplay tries its own driver first, then an available Parsec VDD, then mirroring. Setup downloads Parsec only if the preferred driver fails and Parsec is missing. If an existing Parsec driver is broken, repair it rather than overwriting a shared installation. The Parsec fallback does not add custom resolutions or restart its shared adapter.

**Video lags, freezes for a few seconds or catches up in bursts (older or low-memory tablets).** The tablet may not be able to decode the video at its full panel resolution. Open the settings page: when the tablet is connected, a **Recommended maximum for this tablet** resolution (for example `1280x800`) is shown above the resolution controls, with the reason. It is based on the tablet's RAM and hardware video decoder and is only a suggestion; DrawSplay never applies it by itself. To use it:
- Linux: set the tablet's display to that resolution in the desktop's display settings (GNOME: Settings > Displays; X11 such as Cinnamon: the "Virtual-1-1" display). DrawSplay remembers the choice for that tablet.
- Windows: choose the resolution in Windows Display Settings.
- If the settings page doesn't show a recommendation, the tablet's native size is fine or the app is older; update the tablet app.
- If the stream is still choppy at the recommended size, try a lower one and keep the tablet app in the foreground.

**The resolution keeps changing back.** Reload the settings page (it can be out of date) and set the resolution again; recent versions no longer overwrite a resolution chosen in the OS display settings when you press Save.

**Tablet app stays on "waiting for host" after being reinstalled or restarted.** Update the host; recent versions reconnect automatically when the tablet app is relaunched. If it still doesn't connect, unplug and replug the tablet, or restart the host and reopen the app.

**Tablet shows no prompt / doesn't open when plugged in.** Turn on **Auto-launch Android app on USB connect** on the settings page, or open the app manually.

## Wi-Fi (beta)

Wi-Fi streaming works end to end, display plus touch and pen input, but it is still **in beta**. Expect more latency than USB and occasional hitches on busy or weak networks. **Use USB whenever you can**; Wi-Fi is for when a cable isn't practical.

To use it:

1. Start the host normally. The Wi-Fi section is visible by default but toggled off (set `DRAWSPLAY_FEATURE_WIFI=0` to hide and disable it).
2. On the settings page (`http://127.0.0.1:27180`), turn on **Wi-Fi** in the configuration, choose your network interface and save. A pairing code appears in **Host overview**.
3. On the tablet, tap **Wi-Fi connect**, enter all eight digits, then click
   **Approve** in the computer's pairing card when the tablet says the code is
   verified. Keep both apps open. If pairing fails or expires, request a fresh
   code on the computer and tap **Try again** on the tablet. The tablet switches
   to the display after the first picture arrives and reconnects to the paired
   computer automatically afterwards.

Notes:

- The computer and tablet must be on the same local network. Connections are encrypted (mutual TLS) and only paired devices are accepted.
- USB always takes priority. Plugging in the cable stops the Wi-Fi session, and Wi-Fi resumes when it's unplugged.
- **Host overview** shows a live signal-strength indicator for the tablet's Wi-Fi link. For the best results use a 5 GHz network with a strong signal ("Good" or better). If the image lags or hitches, move closer to the router or switch to USB.
- The stream adapts its bitrate to the link, so quality may drop on a congested network.

**Switch to a different computer:** on the tablet's **Waiting for host** screen,
tap **Forget wireless host** (shown when a Wi-Fi pairing is saved) and confirm.
If the display is already connected, use **Back → Connection options** first.
This stops
reconnecting to that computer and clears the tablet's saved pairing. Then tap
**Wi-Fi connect** and pair with the new computer using its code. USB connections
and saved display settings are unchanged. This forgets the pairing on the tablet
only; it does not remove the tablet from the old computer's saved devices.

**Wireless stays on the waiting screen:** unplug USB to test Wi-Fi; USB
accessory mode, and USB power with USB debugging enabled, take priority over
wireless. Check the computer is running DrawSplay with Wi-Fi enabled and the
correct network interface saved, and both devices are on the same non-guest
network. The tablet distinguishes missing Wi-Fi from unreadable saved trust
or an unavailable Android secure identity. A trust error offers local
forget-and-repair; an identity error may need an Android restart or Keystore
repair and is never silently reset. USB remains available.

Developer tools for Wi-Fi testing are in [`tools/wifi_enrollment`](tools/wifi_enrollment/README.md) and [`tools/android_wifi_probe`](tools/android_wifi_probe/README.md).

## DrawSplay Desktop (PC as the second screen)

`desktop/` holds a host and a client (both Rust, Windows and Linux) that turn one computer into an extra display for another, with mouse, keyboard, touch and pen sent back. It is separate from the Android DrawSplay app and shares no code with it.

- **`dd-host`** runs on the computer whose desktop you extend. It creates an extra monitor, captures it, encodes H.264 and streams it. It injects the input it receives.
- **`dd-client`** runs on the computer that becomes the extra display. It decodes the stream, shows it in a window or fullscreen, and sends input back.
- `desktop/proto/` is the shared wire protocol and the encryption and pairing code.

Any mix works: Windows or Linux host with a Windows or Linux client.

More detail: [`desktop/docs/architecture.md`](desktop/docs/architecture.md) (protocol and session lifecycle) [`desktop/docs/known-issues.md`](desktop/docs/known-issues.md) (limitations and fixed bugs) and [`desktop/docs/open-bugs.md`](desktop/docs/open-bugs.md) (every open bug and unverified item, with locations and suggested fixes).

### Requirements

- `ffmpeg` on the PATH of both computers (capture, encode and decode use it).
- A network path between them: Wi-Fi, Ethernet, or any USB network link (USB bridge cable, Thunderbolt/USB4 networking, Linux `g_ether`). Two PCs cannot talk over a plain USB cable, because neither can act as a USB device.
- **Windows host, extra monitor:** the DrawSplay virtual display driver (installed by the DrawSplay installer). See `desktop/docs/known-issues.md`.
- **Linux host, extra monitor:** an X11 session and a spare RandR output (see "Linux virtual display"), or a Wayland session (see "Wayland"): sway and Hyprland with `wf-recorder`, or GNOME, KDE and others through the desktop portal with `gstreamer1.0-tools` and `gstreamer1.0-pipewire`.
- **Linux host, touch/pen/full keyboard:** access to `/dev/uinput` (see "Linux input").

### Quick start

```
# computer to extend (host)
dd-host

# computer that becomes the display (client)
dd-client HOST_IP --fullscreen          # or: --size 1920x1080 for a window
```

The first connection asks both people to compare a 10-digit code (below). Press `Ctrl+Alt+Shift+Q` in the client to close it, which matters in fullscreen.

Useful options (`--help` lists all): `--encoder auto|software|nvenc|qsv|amf|vaapi`, `--bitrate-kbps N`, `--source virtual|desktop|test`, `--require-virtual`, `--no-hwdec` and `--fit` (client; `--fit` scales the picture to the window, keeping its aspect ratio).

### Pairing and encryption

Everything is encrypted and mutually authenticated (Noise XX, X25519 and ChaCha20-Poly1305). Each machine has a long-term key under `%APPDATA%\DrawSplayDesktop\{host,client}` (or `~/.config/drawsplay-desktop/{host,client}`).

The first time a client connects, both screens show the same 10-digit code. Answer `y` on both machines only if the codes match; the keys are then remembered. If the codes differ, answer `n`: someone is in the middle. The code is bound to random values that each side commits to before it can see the code, so an attacker cannot retry keys until the two codes agree. This changed the wire protocol (version 2): update the host and the client together.

Without a terminal (for example the host as a background service), pair by hand:

```
dd-client --show-key        # on the client: prints its key
dd-host --trust <that key>  # on the host: trusts it
dd-host --show-key          # on the host
dd-client --trust <that key>
```

List trusted devices with `--list-trusted` and remove one with `--forget KEY_PREFIX` (at least 6 hex characters of the key, as printed by the list); both programs support these. The files `trusted_clients.txt` and `trusted_hosts.txt` in the config folder can still be edited by hand. `--trust-all` skips the check and is for testing only.

### Fake displays and the web UI

`dd-host --remove-fake-displays` (Linux) removes every fake display (a spare HDMI/DP connector forced on without a monitor, an active `Virtual*` output, extra headless Wayland outputs) and leaves your real monitors alone; the host also does this at start-up, when the last session ends and when it is stopped, whoever created the display.

While running, `dd-host` writes `status.json` into its config folder; the DrawSplay web UI (Android host) reads it to show a connected PC client and can end that session through a `disconnect-request` file (the client is told not to reconnect).

### Connections and timeouts

- The host serves one client at a time by default; `--max-clients N` (1-8) serves several at once, each on its own virtual monitor, and further clients are refused immediately. Fake displays are cleared only when the last session ends, so one client leaving never removes another's monitor. Each connection is handled on its own thread, so a silent or slow stranger cannot block a real client.
- A stranger gets 15 seconds per step. At most 8 unauthenticated connections are in progress at once, and only one pairing question can be on screen.
- A connected side that hears nothing for 10 seconds ends the session (the client pings every 2 seconds, the host streams continuously). The client then says "host stopped responding". If the connection had been working, the client reconnects by itself (backoff 0.5 s to 5 s, for up to 60 s after the last good connection) and shows the last frame meanwhile; a first connection that fails reports the error at once. Closing the window stops it.
- On a first pairing the host waits up to 5 minutes for the other person to confirm the code.

### Input

| | Windows host | Linux host |
|---|---|---|
| Mouse | yes (moved with `SetCursorPos`, so any monitor placement works) | yes (X11) |
| Keyboard | physical keys by scancode (any layout) | physical keys through uinput; basic keys through X11 without it |
| Touch | native multi-touch (up to 10 fingers) | native multi-touch through uinput |
| Pen | native pen with pressure and tilt | native pen through uinput |
| Without native support | touch and pen act as a left-button mouse | same |

The client reports a pen when the OS gives it an altitude angle, otherwise a touch. Keys and buttons still held when a session ends are released so nothing sticks.

#### Linux input

Create virtual devices through `/dev/uinput`: run `install-linux.sh --udev` from the release package (or copy `99-drawsplay-desktop.rules` to `/etc/udev/rules.d/`, reload udev; if input does not work at once, log out and back in). The rule uses `uaccess`, which gives the user logged in at the console access to `/dev/uinput` and nothing else; you do not need to join the `input` group, which would also let your programs read every keystroke on the machine. A host that runs without a local login (over ssh, as a system service) needs a group of its own instead; see the comments in the rule file. Touch and pen devices span the whole X screen, so positions are scaled against the screen size. This is exact on X11; Wayland compositors may map absolute devices differently.

### Linux virtual display

On X11 the host lights up a spare RandR output as an extra monitor (preferring outputs named `VIRTUAL*`, then disconnected ports; it tries each in turn until one accepts). Ctrl+C or SIGTERM switches the output off again. Many GPU drivers refuse to enable a port with nothing plugged in; in that case use an HDMI/DisplayPort dummy plug, the `vkms` kernel module, or force the connector with the `video=` kernel parameter. When no virtual display can be created the host says so and mirrors the primary screen instead (`--require-virtual` makes it fail instead). Wayland sessions are not supported yet.

### Building

Run these from the `desktop/` folder (the repository-wide build notes are in [`BUILD.md`](BUILD.md)).

```
cargo build --release          # dd-host and dd-client in target/release
cargo test                     # unit tests
packaging/package-windows.ps1  # dist/DrawSplayDesktop-windows-x86_64-vX.zip
packaging/package-linux.sh     # dist/DrawSplayDesktop-linux-x86_64-vX.tar.gz
```

Both packages include the project README and the `docs/` folder.

#### Wayland

The host picks a backend from the session:

- **sway and Hyprland:** the extra monitor is created through the compositor (sway: `swaymsg create_output`, which sway only allows on its headless or nested backends; Hyprland: `hyprctl output create headless`) and captured with `wf-recorder`. When no virtual output can be made the host mirrors the primary output. Mouse input uses an absolute `uinput` device, so `/dev/uinput` access is required as for touch and pen.
- **GNOME, KDE and other desktops (xdg-desktop-portal):** the host asks the desktop's ScreenCast and RemoteDesktop portals for a screen. Your desktop shows its own "share your screen" dialog the first time each session starts; accept it on the host. A virtual monitor is requested when the portal offers one (its size is chosen by the compositor and reported to the client), otherwise you pick a monitor in the dialog and it is mirrored. The screen is read with `gst-launch-1.0 pipewiresrc` (packages `gstreamer1.0-tools`, `gstreamer1.0-pipewire`). Keyboard, mouse and touch go through the portal; a pen uses `uinput` when available, else acts as a finger. If the portal has no RemoteDesktop, only the screen is taken and input falls back to `uinput`.

Verified: sway on a headless compositor, the portal protocol against a mock portal, and the capture pipeline against a real PipeWire node. **Not run against a real GNOME or KDE session yet**; expect rough edges (see `desktop/docs/open-bugs.md`, DD-004).

#### ARM

Both programs build for `aarch64-unknown-linux-gnu` (verified: cross-built from x86_64; `dd-proto` and `dd-client` unit tests pass under qemu-user). Cross-build recipe on Debian/Ubuntu: `rustup target add aarch64-unknown-linux-gnu`, install `gcc-aarch64-linux-gnu libc6-dev-arm64-cross`, make the arm64 `libxdo.so` visible to the linker (for example in a private sysroot passed with `-C link-arg=-L...`), then `TARGET=aarch64-unknown-linux-gnu packaging/package-linux.sh`. On a native ARM machine just use the normal build and `package-linux.sh`. Windows on ARM: `packaging/package-windows.ps1 -Target aarch64-pc-windows-msvc` (not built or run yet; the virtual display driver's `build.ps1 -Arch arm64` exists but has not been exercised).

Tests: `cargo test --release` runs unit tests and `desktop/host/tests/loopback.rs`, which starts a real `dd-host --source test` and drives it with a scripted client (needs `ffmpeg`; those tests skip without it). The host honours `DD_IDLE_TIMEOUT_MS` and `DD_STEP_TIMEOUT_MS` to shorten timeouts for testing.

Linux build dependencies: `pkg-config libx11-dev libxkbcommon-dev libwayland-dev libxcursor-dev libxi-dev libxrandr-dev libxdo-dev libxtst-dev libudev-dev`.

From `desktop/`, `cargo run -p dd-client --example send_input -- HOST:PORT` replays a scripted pen stroke, two-finger touch and a modifier key, to test host injection without a touchscreen.

### What has and has not been verified

Verified: protocol, encryption and pairing (unit tests, and live on Windows and in a Linux container, including a slow second person at first pairing); connection handling (a silent stranger does not block a real client, a killed client frees the host, a frozen host is detected by the client after 10 s); streaming and decoding on both OSes (about 60 fps at 1080p, 0.2 ms loopback round trip with NVENC, measured before the latest changes); the Windows virtual monitor (plug, extend, requested size, removal); Windows pen position tracking and touch injection acceptance; Linux fallback paths under Xvfb; Linux `uinput` device creation in the kernel; scancode, CVT mode-line and xrandr parsing tables.

Not verified: the mouse path (`SetCursorPos`) and the driver connector-slot fix on a live Windows virtual monitor; Windows display scaling other than 100% (the host warns at start-up); the Linux virtual display on real hardware (needs a GPU with a spare output; only parsing and mode-line maths are tested); Linux `uinput` event delivery (the test kernel has no evdev handler to read events back); Windows keyboard injection into a real application; Wayland; macOS (not supported); real-network behaviour such as packet loss.

### Known limitations

- Pen tilt is one-dimensional (the OS gives the client only the pen's altitude angle).
- One client at a time per host unless `--max-clients` is raised (not tried on real hardware; Windows allows at most 8 monitors).
- The image is shown 1:1 (never scaled); if the client window is resized, the picture is centered.
- Video is cut into network chunks rather than whole frames, and keyframes cannot be requested. TCP is reliable, and every session starts a fresh encoder, so a client always sees the stream from its first frame.
- No audio.
- The pairing prompt reads the terminal (see manual pairing above for services).
- See `desktop/docs/known-issues.md` for the Windows driver limit and the list of open improvements.

## Privacy

The host listens only on your computer (localhost) and the settings page is local-only. In Wi-Fi mode (beta) the host connects out to the paired tablet on your local network over encrypted, mutually authenticated TLS. See [PRIVACY.md](PRIVACY.md) for the full privacy policy.

## License

DrawSplay is licensed under the GNU General Public License v3.0. See [LICENSE](LICENSE).

DrawSplay bundles a Windows virtual display driver derived from Microsoft's IddCx sample under the Microsoft Public License (MS-PL); see [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
