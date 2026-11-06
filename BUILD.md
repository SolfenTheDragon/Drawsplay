# DrawSplay Build Guide

This guide walks through building DrawSplay:

1. `host/` (Go desktop host service)
2. `android/` (Android tablet app)
3. `native/` (optional Linux and Windows media helpers)
4. `desktop/` (DrawSplay Desktop, the separate Rust host and client)

Paths under `docs/` and `desktop/docs/` are maintainer notes kept out of the public repository.

## Prerequisites

### Host (Windows or Linux)

- Go **1.26+** (`host/go.mod` says `go 1.26.0` with `toolchain go1.26.10`; an older Go downloads that toolchain automatically unless `GOTOOLCHAIN=local` is set)
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

When the legacy ADB transport is enabled, the host prefers a bundled ADB copy. It does not resolve or start ADB when that transport is off (the default). For ffmpeg, the host prefers a bundled copy. On Windows the installer always downloads the newest ffmpeg release compatible with the OS and CPU architecture (GitHub release API: `GyanD/codexffmpeg` essentials for x64, `BtbN/FFmpeg-Builds` newest stable `nX.Y` for x64 fallback and ARM64), verifies the release's SHA-256 digest, and writes `runtime\ffmpeg\drawsplay-runtime.json` listing the SHA-256 of every installed `.exe`/`.dll`; the host verifies the bundled copy against that manifest before use. adb is never auto-downloaded; supply `runtime\adb\adb.exe` or a PATH adb yourself if you enable the legacy ADB transport.

### Android app

- Android Studio (recommended), or Gradle with Android SDK installed
- Java/JDK **17**
- Android SDK Platform **36** (`compileSdk`/`targetSdk` 36, `minSdk` 26) and matching Build Tools
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

The Windows host embeds DrawSplay's own IddCx virtual display driver (`host/driver/idd/DrawSplayIdd.dll`/`.inf`/`.cat`) via `go:embed` as the preferred backend. Parsec VDD is an on-demand fallback, not bundled or downloaded when the preferred driver works. Prebuilt artifacts are committed, so a plain `go build` works. To rebuild them from source (`drivers/windows/idd`), generate and sign the catalog, and stage them into `host/driver/idd`, run:

```powershell
.\scripts\build-idd-driver.ps1        # add -WdkRoot <folder> to skip the WDK NuGet restore
```

It needs Visual Studio 2022 (C++ toolset) and the Windows SDK; the WDK comes from the `Microsoft.Windows.WDK.x64` NuGet package automatically. Optional build-time catalog signing uses the same `DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH`/`_PASSWORD` as the host. `scripts/build-release.ps1` runs this step automatically before the host build. The elevated installer signs an unsigned embedded catalog locally using in-box Windows PowerShell, adds a per-PC certificate to LocalMachine Root/TrustedPublisher, and deletes its private key immediately. No SDK, certificate purchase, or Microsoft submission is needed on the user's PC for that path. An already trusted catalog is preserved; invalid signed catalogs are rejected rather than re-signed. Windows security policies can still reject a locally signed driver.

Local PFX catalog signing is not the Microsoft driver-submission process and
does not by itself prove the driver will install/load under the target PC's
security policy. The step-by-step driver signing guide (`docs/windows-driver-signing.md`)
covers Microsoft signing and the manual packaging path. Do not run the normal
release script over a returned Microsoft-signed package: it can regenerate
and replace the signed driver artifacts.

The opt-in Windows certificate-lifecycle integration test creates temporary
machine trust, verifies signing/key deletion and preservation of a trusted
catalog, then removes its certificate. Run only in an administrator PowerShell:

```powershell
cd host
$env:DRAWSPLAY_TEST_DRIVER_SIGNING = "1"
go test -run '^TestIDDLocalSigningIntegration$' .
Remove-Item Env:\DRAWSPLAY_TEST_DRIVER_SIGNING
```

Normal `go test` skips that machine-modifying test. It never creates a display
device; driver installation and monitor creation require separate runtime checks.

The driver staging hardening check also requires an administrator PowerShell:

```powershell
$env:DRAWSPLAY_TEST_DRIVER_STAGING = "1"
go test -run '^TestDriverStagingIntegration$' .
Remove-Item Env:\DRAWSPLAY_TEST_DRIVER_STAGING
```

It creates an admin-only workspace with its final owner/DACL set atomically,
checks a privilege-disabled token (Administrators deny-only) cannot modify
files or directory permissions, generates/signs the WinUSB catalog, then
removes the temporary trust and workspace. It does not install a package or
change tablet bindings. Normal unit tests reject unsafe ACLs, substituted INF
bytes and user-writable signing paths without creating machine trust.

After installation, the following opt-in test briefly creates/removes a real
desktop output. Run from `host/` without an active tablet session and save work first:

```powershell
$env:DRAWSPLAY_TEST_INSTALLED_IDD = "1"
go test -run '^TestIDDInstalledMonitorIntegration$' .
Remove-Item Env:\DRAWSPLAY_TEST_INSTALLED_IDD
```

For an already installed Parsec VDD, the corresponding non-elevated check
creates/removes a real fallback monitor and exercises its keepalive. It never
downloads, installs, disables or restarts a driver. Run with no active session:

```powershell
$env:DRAWSPLAY_TEST_INSTALLED_PARSEC = "1"
go test -run '^TestParsecInstalledMonitorIntegration$' .
Remove-Item Env:\DRAWSPLAY_TEST_INSTALLED_PARSEC
```

Fallback setup pins Parsec VDD 0.41.0.0 in `host/parsec_install_windows.go`
(URL, SHA-256 and Authenticode publisher); updating it requires validating all
three and Windows compatibility. It is downloaded only after DrawSplay driver
failure, when Parsec is absent and automatic installation is allowed.

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

Place `drawsplay-media` next to the host executable (or set `DRAWSPLAY_MEDIA_HELPER`). Without it the host uses ffmpeg exactly as before. CI builds it with `make CC="cc -Werror"`, so keep it warning-free.

## Build the native media helper (Windows)

`scripts/build-release.ps1` builds it and stages it into `host/mediahelper/` for `go:embed`. To build it by hand (Visual Studio or the C++ Build Tools; no WDK needed):

```powershell
native\drawsplay-media-win\build.ps1 -OutDir host\mediahelper   # embed into the next host build
# or: -OutDir <folder of host.exe> to run it next to an existing host
native\drawsplay-media-win\out\drawsplay-media.exe --probe       # finds a GPU H.264 encoder?
native\drawsplay-media-win\out\drawsplay-media.exe --selftest > frames.bin
```

It needs the frame-export IddCx driver from this repository; with an older driver it exits at once and the host uses ffmpeg.

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

1. Connect the tablet by USB (no USB debugging needed for the default AOA connection).
2. Start host:
   - Windows: `.\host\host.exe`
   - Linux: `./host/host`
3. Install/run Android debug app on the USB device from Android Studio, or (this step needs USB debugging):
   ```powershell
   adb install -r android\app\build\outputs\apk\debug\app-debug.apk
   ```

With the default settings, the app and host connect over AOA; USB debugging is not used for the connection.

## Packaging and installer scripts

### Where each packaging file is used

| Path | What it is | Used by |
|---|---|---|
| `packaging/linux/70-drawsplay-aoa.rules` | udev: tablet USB access for the accessory (AOA) transport | `scripts/install-host-linux.sh`, release tarball |
| `packaging/linux/70-drawsplay-drm-connector.rules` | udev: lets `plugdev` force a spare HDMI port on | `scripts/install-host-linux.sh`, release tarball |
| `packaging/linux/99-drawsplay-uinput.rules` | udev: `/dev/uinput` for the logged-in session (pen, touch, keyboard) | both `scripts/install-host-linux.sh` and `desktop/packaging/install-linux.sh` |
| `packaging/linux/drawsplay-display-helper`, `com.drawsplay.display-helper.policy` | root helper and polkit action for X11 virtual displays | `scripts/install-host-linux.sh` |
| `packaging/windows/drawsplay-aoa.inf` | WinUSB binding template for the accessory identity (must match `host/aoa_inf.go`) | reference for the generated package |
| `scripts/install-host-linux.sh`, `scripts/uninstall-host-linux.sh` | install / remove the Android-tablet host on Linux | users, release tarball |
| `scripts/install-host-windows.ps1` | developer install of the host on Windows | developers (users run the installer exe) |
| `scripts/build-release.ps1` | Windows release build (host, media helper, installer) | CI `release.yml` |
| `scripts/build-idd-driver.ps1`, `drivers/windows/idd/build.ps1` | build the IddCx driver and copy it into `host/driver/idd/` | developers with the WDK |
| `scripts/build-native-media.sh` | build the Linux native media helper | developers |
| `desktop/packaging/package-linux.sh`, `package-windows.ps1` | DrawSplay Desktop release archives | CI `release.yml` |
| `desktop/packaging/install-linux.sh` | install `dd-host` / `dd-client` for the current user | DrawSplay Desktop users |

### Windows release packaging

From the repository root:

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

In CI, signing is optional unless the repository variable `DRAWSPLAY_REQUIRE_SIGNING` is `1`; partially configured secrets always fail the build. See [SIGNING.md](SIGNING.md).

### Linux and USB accessory (AOA) packaging files

- `packaging/linux/70-drawsplay-aoa.rules`: udev rule granting the logged-in user (`uaccess`) access to Android devices for the ADB-free transport. Install to `/etc/udev/rules.d/`. The per-vendor lines skip devices that expose HID, printer, mass-storage, hub or wireless interfaces, so a tablet maker's keyboards, drives or dongles are not opened up. Comments must stay on their own lines: udev ignores a rule line with a trailing comment.
- `packaging/linux/70-drawsplay-drm-connector.rules`: makes each HDMI connector's `status` file writable by the `plugdev` group, so the host can force a spare port on for the X11 virtual display without a password prompt.
- `packaging/linux/99-drawsplay-uinput.rules`: grants the logged-in session (`uaccess`) access to `/dev/uinput` for pen, touch and keyboard injection. Both the host installer and DrawSplay Desktop's `install-linux.sh --udev` install this same file; the host uninstaller keeps it while `dd-host` is installed.
- `packaging/linux/drawsplay-display-helper` and `com.drawsplay.display-helper.policy`: the fixed-purpose root helper (installed to `/usr/libexec/drawsplay/`) and its polkit action. The host runs root steps (forcing a connector, loading `vkms`) through it, so the password prompt names DrawSplay; without it the host falls back to a generic `pkexec` prompt.
- `packaging/windows/drawsplay-aoa.inf`: WinUSB binding for the accessory identity. The tablet's normal-mode identity also needs WinUSB for AOA switching; the installed host generates and signs a WinUSB package on the user's PC for both (per-PC self-signed certificate, private key deleted after signing), so no build-time driver signing is needed.

## GitHub release automation

- CI/build workflow: `.github/workflows/ci.yml`. On every push it lints the workflows (actionlint), runs `go vet`, `go test -race` and `govulncheck` on the host, runs the host tests on Windows, runs `cargo fmt --check`, `cargo clippy -D warnings` and `cargo test` on DrawSplay Desktop, builds both native media helpers, and builds and tests the Android app. Third-party actions are pinned to commit SHAs; update the SHA and its version comment together.
- Tag-triggered release publishing workflow: `.github/workflows/release.yml`

Publishing a new GitHub release with auto-built EXE/APK assets:

```bash
git tag v0.2.0
git push origin v0.2.0
```

You can also run the **Release** workflow manually (`workflow_dispatch`). It
takes no input: it picks the highest stable `vX.Y.Z` tag, increments the patch
number (`v0.2.0` becomes `v0.2.1`), and creates that tag only after the builds
succeed.

The release workflow builds host + Android release artifacts and attaches:

- `DrawSplay-windows-<tag>.zip`: a folder with `drawsplay_installer_<tag>.exe` and `DrawSplay.APK` (a `resolve-tag` job computes the tag first so the Windows build can name the installer)
- `DrawSplay-linux-<tag>.tar.gz`: a folder with the Linux host, install/uninstall scripts, tray icon, both udev rules, the display helper with its polkit policy, and `DrawSplay.APK`
- `DrawSplayDesktop-windows-x86_64-<tag>.zip` and `DrawSplayDesktop-linux-x86_64-<tag>.tar.gz`: the separate DrawSplay Desktop host and client (built by the `build-desktop-windows` and `build-desktop-linux` jobs, which run `cargo test --release` first and then `desktop/packaging/package-*`; the tag is passed to the scripts as the version)
- `DrawSplay.APK`
- `SHA256SUMS.txt`

GitHub secrets for signed CI releases:

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

Installs the host binary to `~/.local/bin/drawsplay-host`, adds a launcher and login autostart, installs missing runtime packages with `apt` (skip with `--no-install-deps`), and asks for your password to install the uinput rule, the USB udev rule and the display helper with its polkit action. `scripts/uninstall-host-linux.sh` removes them again.

### Optional Wi-Fi diagnostics

Wi-Fi numeric pairing uses pinned `1Password/srp` and Bouncy Castle
lightweight SRP implementations; see `docs/wifi-pairing.md`
for the exact suite and interoperability caveat. Only Wi-Fi enrollment code
loads those classes; no global Android security provider is installed.

The `:app:assembleWifiTest` Android variant builds a **separate**
`com.drawsplay.tablet.wifitest` APK with both USB attachment activities
removed. It is for isolated real-device Keystore, LAN enrollment and paired
control/video channel tests, not distribution or
replacement of the production app. Normal release artifacts are unchanged
in application ID; never install the test variant over another test
installation without checking its ownership first.

CI builds `host/cmd/wifi-pairing-test-peer` and supplies its path in
`DRAWSPLAY_PAIRING_TEST_PEER` to Android tests for Go/Kotlin interoperability.
The peer uses public synthetic test credentials over stdio. Its optional
network-enrollment and two-channel session modes dial numeric loopback addresses only, with a
disposable identity and no paired trust persistence. It cannot listen or
dial the LAN.

The opt-in Linux enrollment preview builds with
`go build -o wifi-pair ./cmd/wifi-pair` from `host/`. It performs explicit
selected-interface discovery, PAKE, mutual TLS and local approval, but no
capture/decoder/input session. Add `-verify-session` to verify fresh paired
control/video association without media. The real-device automation is
`tools/wifi_enrollment/run_tablet_test.py`; see `docs/wifi-pairing.md`.
The tablet hardware probe requires API 33 for network-scoped NSD; the
enrollment and session codecs still target API 26. The automation's matching
option is `--verify-session`.

The independent discovery command builds with the host module:

```bash
cd host
go build -o wifi-discover ./cmd/wifi-discover
go test -race ./internal/wifilan ./cmd/wifi-discover
```

It is not bundled into or started by the normal host service. See
`docs/wifi-discovery.md` for required explicit LAN
interface selection and current limitations.

The separate Android advertisement probe is built without modifying the
production app: `bash tools/android_wifi_probe/build.sh NEW_OUTPUT_DIRECTORY`.
It requires JDK 17, installed SDK platform 34 and build-tools 36.0.0.
The script does not download tools or install an APK, and deletes its
temporary private signing key. Its network-scoped NSD experiment requires
API 33+ at runtime; it reports a limitation and exits on older Android.
See [`tools/android_wifi_probe/README.md`](tools/android_wifi_probe/README.md)
for installation, testing and cleanup of the distinct test package.

## Build DrawSplay Desktop (Rust)

`desktop/` is a standalone Cargo workspace (`dd-proto`, `dd-host`, `dd-client`) that does not depend on the Go host or the Android app.

Prerequisites: a current stable Rust toolchain (`rustup`), and `ffmpeg` on the PATH at run time. On Linux also install the build libraries: `pkg-config libx11-dev libxkbcommon-dev libwayland-dev libxcursor-dev libxi-dev libxrandr-dev libxdo-dev libxtst-dev libudev-dev`.

```
cd desktop
cargo build --release            # target/release/dd-host and dd-client
cargo test                       # unit tests
cargo fmt --check                # formatting (checked in CI)
cargo clippy --all-targets -- -D warnings   # lint (CI fails on any warning)
packaging/package-windows.ps1    # dist/DrawSplayDesktop-windows-x86_64-vX.zip
packaging/package-linux.sh       # dist/DrawSplayDesktop-linux-x86_64-vX.tar.gz
```

Linux builds and tests can also be run in a container (for example `rust:1-bookworm` with the packages above); copy the sources in with `docker cp` or `tar` rather than a bind mount if the drive is not shared. Windows virtual-display support needs the DrawSplay IddCx driver from `drivers/windows/idd` to be installed; rebuild, re-sign and reinstall it to pick up the connector-slot fix and the rule that a caller may remove only the monitors it created (administrators may remove any).

**Tests.** `cargo test --release` runs the unit tests and `desktop/host/tests/loopback.rs`, which starts a real `dd-host --source test` and drives it with a scripted client (needs `ffmpeg`; those tests skip without it). The host honours `DD_IDLE_TIMEOUT_MS` and `DD_STEP_TIMEOUT_MS` to shorten timeouts for testing. From `desktop/`, `cargo run -p dd-client --example send_input -- HOST:PORT` replays a scripted pen stroke, two-finger touch and a modifier key, to test host injection without a touchscreen.

Both packages include the repository README (and `desktop/docs/` when it is present in the source tree; it is not part of the public repository).

### DrawSplay Desktop on ARM

Both programs build for `aarch64-unknown-linux-gnu` (verified: cross-built from x86_64; `dd-proto` and `dd-client` unit tests pass under qemu-user). Cross-build recipe on Debian/Ubuntu: `rustup target add aarch64-unknown-linux-gnu`, install `gcc-aarch64-linux-gnu libc6-dev-arm64-cross`, make the arm64 `libxdo.so` visible to the linker (for example in a private sysroot passed with `-C link-arg=-L...`), then `TARGET=aarch64-unknown-linux-gnu packaging/package-linux.sh`. On a native ARM machine just use the normal build and `package-linux.sh`. Windows on ARM: `packaging/package-windows.ps1 -Target aarch64-pc-windows-msvc` (not built or run yet; the virtual display driver's `build.ps1 -Arch arm64` exists but has not been exercised).
