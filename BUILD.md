# DrawSplay Build Guide

This guide walks through building both parts of DrawSplay:

1. `host/` (Go desktop host service)
2. `android/` (Android tablet app)

## Prerequisites

### Host (Windows or Linux)

- Go **1.25+** (`host/go.mod` targets `go 1.25.0`)
- `ffmpeg` on your PATH (required at runtime for video streaming)
- `adb` from Android Platform Tools is optional and only needed for the legacy ADB-reverse transport or app installation/debugging.

Linux Wayland hosts should also have GStreamer PipeWire capture support and Android udev rules. On Ubuntu:

```bash
sudo apt install android-sdk-platform-tools android-sdk-platform-tools-common ffmpeg gstreamer1.0-tools gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-pipewire
```

Also install a VA-API GPU driver so the host encodes on the GPU (`vah264enc`/`vaapih264enc`) instead of falling back to a lower-quality software x264 profile:

```bash
sudo apt install intel-media-va-driver-non-free vainfo   # Intel integrated graphics
# or: sudo apt install mesa-va-drivers vainfo            # AMD graphics
```

For production packaging, you can bundle:

- `runtime\adb\adb.exe`
- `runtime\ffmpeg\ffmpeg.exe` (and ffmpeg runtime DLLs)

When the legacy ADB transport is enabled, the host prefers a bundled ADB copy. It does not resolve or start ADB when that transport is off (the default). For ffmpeg, the host prefers a bundled copy. On Windows the installer always downloads the newest ffmpeg release compatible with the OS and CPU architecture (GitHub release API: `GyanD/codexffmpeg` essentials for x64, `BtbN/FFmpeg-Builds` newest stable `nX.Y` for x64 fallback and ARM64), verifies the release's SHA-256 digest, and writes `runtime\ffmpeg\drawsplay-runtime.json` listing the SHA-256 of every installed `.exe`/`.dll`; the host verifies the bundled copy against that manifest before use. adb is never auto-downloaded ? supply `runtime\adb\adb.exe` or a PATH adb yourself if you enable the legacy ADB transport.

### Android app

- Android Studio (recommended), or Gradle with Android SDK installed
- Java/JDK **17**
- Android SDK Platform **34** and Build Tools for API 34
- A USB-connected Android tablet/device; Developer Options and USB debugging are not required for AOA

## Build the host service

From the repository root:

### Windows (PowerShell)

```powershell
cd host
go mod download
go build -o host.exe .
```

The Web UI footer shows the host version, injected at release time with `-ldflags "-X main.version=<tag>"` (local builds show `dev-<commit>`) and links to the matching GitHub release.

Output binary: `host\host.exe`. This is a console build (handy for development; run with `-background=false` to log to the terminal). Release builds use `go build -ldflags "-H=windowsgui"` (see `scripts/build-release.ps1`) so the host never opens a console window and runs in the system tray.

#### Bundled virtual display driver

The Windows host embeds DrawSplay's own IddCx virtual display driver (`host/driver/idd/DrawSplayIdd.dll`/`.inf`/`.cat`) via `go:embed`, replacing the former third-party Parsec VDD dependency. Prebuilt artifacts are committed, so a plain `go build` works. To rebuild them from source (`drivers/windows/idd`), generate and sign the catalog, and stage them into `host/driver/idd`, run:

```powershell
.\scripts\build-idd-driver.ps1        # add -WdkRoot <folder> to skip the WDK NuGet restore
```

It needs Visual Studio 2022 (C++ toolset) and the Windows SDK; the WDK comes from the `Microsoft.Windows.WDK.x64` NuGet package automatically. Catalog signing uses the same `DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH`/`_PASSWORD` as the host (required for the driver to install under Secure Boot). `scripts/build-release.ps1` runs this step automatically before the host build. A build without the embedded files runs in mirror-only mode (no extend-mode virtual display).

### Linux

```bash
cd host
go mod download
go build -o host .
```

Output binary: `host/host`

## Build the native media helper (optional, Linux)

Needs libva, libva-drm, libX11, libXext, libXdamage, libXfixes and zlib development files:

```bash
scripts/build-native-media.sh                 # DRAWSPLAY_HELPER_DEST=host/ to copy next to the host binary
```

Place `drawsplay-media` next to the host executable (or set `DRAWSPLAY_MEDIA_HELPER`). Without it the host uses ffmpeg exactly as before. See `native/drawsplay-media/README.md`.

## Build the Android app

From the repository root:

### Option A: Android Studio (recommended)

1. Open the `android/` folder in Android Studio.
2. Let Gradle sync finish.
3. Build the debug APK from **Build → Build Bundle(s) / APK(s) → Build APK(s)**.

Debug APK output:
`android/app/build/outputs/apk/debug/app-debug.apk`

### Option B: Command line (Gradle)

```powershell
cd android
gradle assembleDebug
```

If your environment has no global `gradle` command, run the build from Android Studio instead.

Debug APK output:
`android/app/build/outputs/apk/debug/app-debug.apk`

## Quick post-build run

1. Confirm device connection:
   ```powershell
   adb devices
   ```
2. Start host:
   - Windows: `.\host\host.exe`
   - Linux: `./host/host`
3. Install/run Android debug app on the USB device from Android Studio, or:
   ```powershell
   adb install -r android\app\build\outputs\apk\debug\app-debug.apk
   ```

With the default settings, the app and host connect over AOA; USB debugging is not used for the connection.

## Packaging and installer scripts

From the repository root:

### Windows release packaging

```powershell
.\scripts\build-release.ps1
```

Outputs (`-Version v1.2.3` sets the installer name; it defaults to the latest `v*` tag, else `dev`):

- `release\drawsplay_installer_<version>.exe` (the Windows installer; it installs itself as `DrawSplay.exe` plus `DrawSplay-Uninstall.exe` in Program Files)
- `release\DrawSplay.APK`
- `release\DrawSplay-host-windows.zip`
- `release\DrawSplay-android-release.zip`
- `release\SHA256SUMS.txt`

### Code signing (production)

`scripts\build-release.ps1` supports Windows Authenticode signing when these
environment variables are set:

- `DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH`
- `DRAWSPLAY_WINDOWS_SIGNING_PFX_PASSWORD`
- optional: `DRAWSPLAY_WINDOWS_SIGNING_TIMESTAMP_URL`

To enforce signing (fail if missing), set:

- `DRAWSPLAY_REQUIRE_SIGNING=1`

The same certificate signs both the host `host.exe` and the bundled IddCx
driver catalog (`host/driver/idd/DrawSplayIdd.cat`).

#### Windows Defender / SmartScreen

An unsigned installer that creates a virtual-display driver, binds WinUSB and
drops shortcuts trips Defender heuristics. The installer avoids the worst
signals in-process (native SetupAPI/`pnputil` driver install with no network
download, native `IShellLinkW` COM shortcut creation, no `powershell
-ExecutionPolicy Bypass`), but the WinUSB per-PC self-signed-certificate flow
(`host/winusb_sign_windows.go`) still looks suspicious to behavioral scanners.
The definitive fix is signing the released `host.exe` with a reputable
public-CA (ideally EV) code-signing certificate so SmartScreen/Defender trust
it by reputation; configure the certificate via the variables above.

Android release signing uses Gradle release keystore settings from either
Gradle properties or environment variables:

- `DRAWSPLAY_ANDROID_KEYSTORE_FILE`
- `DRAWSPLAY_ANDROID_KEYSTORE_PASSWORD`
- `DRAWSPLAY_ANDROID_KEY_ALIAS`
- `DRAWSPLAY_ANDROID_KEY_PASSWORD`

In CI, release signing is enforced and missing values fail the build.

### USB accessory (AOA) packaging files

- `packaging/linux/70-drawsplay-aoa.rules`: udev rule granting the logged-in user access to Android devices for the ADB-free transport. Install to `/etc/udev/rules.d/`.
- `packaging/windows/drawsplay-aoa.inf` and `README.md`: WinUSB binding for the accessory identity. The tablet's normal-mode identity also needs WinUSB for AOA switching; the installed host generates and signs a WinUSB package on the user's PC for both (per-PC self-signed certificate, private key deleted after signing), so no build-time driver signing is needed.

## GitHub release automation

- CI/build workflow: `.github/workflows/ci.yml`
- Tag-triggered release publishing workflow: `.github/workflows/release.yml`

Publishing a new GitHub release with auto-built EXE/APK assets:

```bash
git tag v0.2.0
git push origin v0.2.0
```

You can also run the **Release** workflow manually (`workflow_dispatch`) and
provide `version` (for example `0.2.0`); it will create/push `v0.2.0` if needed
and publish the GitHub Release assets automatically.

The release workflow builds host + Android release artifacts and attaches:

- `DrawSplay-windows-<tag>.zip`: a folder with `drawsplay_installer_<tag>.exe` and `DrawSplay.APK` (a `resolve-tag` job computes the tag first so the Windows build can name the installer)
- `DrawSplay-linux-<tag>.tar.gz`: a folder with the Linux host, install/uninstall scripts, tray icon, udev rule and `DrawSplay.APK`
- `DrawSplay.APK`
- `SHA256SUMS.txt`

Required GitHub secrets for signed CI releases:

- `WINDOWS_SIGNING_PFX_BASE64`
- `WINDOWS_SIGNING_PFX_PASSWORD`
- optional: `WINDOWS_SIGNING_TIMESTAMP_URL`
- `ANDROID_KEYSTORE_BASE64`
- `ANDROID_KEYSTORE_PASSWORD`
- `ANDROID_KEY_ALIAS`
- `ANDROID_KEY_PASSWORD`

### Windows host install helper

```powershell
.\scripts\install-host-windows.ps1
```

Runs the newest `release\drawsplay_installer_*.exe` (or `-SourceExe <path>`) with `-install -quiet` (one UAC prompt): binaries go to `%ProgramFiles%\DrawSplay` (with `DrawSplay-Uninstall.exe`), per-user state/settings to `%APPDATA%\DrawSplay`, and the host starts in the tray at sign-in. Pass `-NoDriverInstall` to skip the virtual display driver, `-DesktopShortcut` to add a desktop shortcut and `-NoAutostart` to not start at sign-in.

### Linux host install helper

```bash
./scripts/install-host-linux.sh
```

Installs the host binary to `~/.local/bin/drawsplay-host`, enables user-session autostart, and prints Linux dependency/permission remediation commands (ADB udev + uinput mapping prerequisites).

### Optional Wi-Fi diagnostics

The independent discovery command builds with the existing Go 1.25 module:

```bash
cd host
go build -o wifi-discover ./cmd/wifi-discover
go test -race ./internal/wifilan ./cmd/wifi-discover
```

It is not bundled into or started by the normal host service. See
[`docs/wifi-discovery.md`](docs/wifi-discovery.md) for required explicit LAN
interface selection and current limitations.

The separate Android advertisement probe is built without modifying the
production app: `bash tools/android_wifi_probe/build.sh NEW_OUTPUT_DIRECTORY`.
It requires JDK 17, installed SDK platform 34 and build-tools 36.0.0.
The script does not download tools or install an APK, and deletes its
temporary private signing key. Its network-scoped NSD experiment requires
API 33+ at runtime; it reports a limitation and exits on older Android.
See [`tools/android_wifi_probe/README.md`](tools/android_wifi_probe/README.md)
for installation, testing and cleanup of the distinct test package.
