# Third-party notices

DrawSplay includes third-party components under their own licenses.

## DrawSplay IddCx virtual display driver (`drivers/windows/idd`)

DrawSplay's bundled Windows virtual display driver is derived from Microsoft's
IndirectDisplay / IddSampleDriver sample (part of the
[Windows driver samples](https://github.com/microsoft/Windows-driver-samples)),
which is licensed under the **Microsoft Public License (MS-PL)**.

- Copyright (c) Microsoft Corporation.
- License text: [`drivers/windows/idd/LICENSE`](drivers/windows/idd/LICENSE).

Per MS-PL section 3(C), the copyright and attribution notices from the original
sample are retained in the source. The compiled driver (`DrawSplayIdd.dll` and
its catalog) is distributed in object-code form under a license that complies
with the MS-PL, as required by section 3(D). DrawSplay's own modifications are
part of the DrawSplay project and licensed under the GPL-3.0 (see
[`LICENSE`](LICENSE)); the MS-PL continues to govern the portions derived from
the Microsoft sample.

## DrawSplay Desktop (`desktop/`) Rust dependencies

The Rust components link the following direct dependencies, each under its own permissive license (transitive dependencies are listed by `cargo metadata` / `cargo tree` in `desktop/`):

| Crate | License |
|-------|---------|
| `snow` (Noise protocol) | Apache-2.0 OR MIT |
| `winit` (window and input events) | Apache-2.0 |
| `softbuffer` (window pixel buffer) | MIT OR Apache-2.0 |
| `bytemuck` | Zlib OR Apache-2.0 OR MIT |
| `font8x8` (launcher bitmap font) | MIT |
| `blake2` (pairing-code commitment hash) | MIT OR Apache-2.0 |
| `enigo` (mouse and key injection) | MIT |
| `evdev` (Linux uinput) | Apache-2.0 OR MIT |
| `ctrlc` (Linux signal handling) | MIT/Apache-2.0 |
| `windows` (Win32 bindings) | MIT OR Apache-2.0 |
| `getrandom` (OS random numbers) | MIT OR Apache-2.0 |
| `zeroize` (key wiping) | Apache-2.0 OR MIT |
| `serde`, `serde_json` (control endpoint and state files) | MIT OR Apache-2.0 |
| `libc` | MIT OR Apache-2.0 |
| `zbus` (Linux D-Bus, Wayland displays) | MIT |

`ffmpeg` is not bundled; it is installed separately and used as an external program.

## DrawSplay host (`host/`) Go modules

The host binary statically links these Go modules (versions in `host/go.mod`):

| Module | License |
|--------|---------|
| `fyne.io/systray` (Windows/Linux tray icon) | Apache-2.0 |
| `github.com/1Password/srp` (Wi-Fi pairing PAKE) | Apache-2.0 |
| `github.com/godbus/dbus/v5` (Linux D-Bus) | BSD-2-Clause |
| `github.com/miekg/dns` (Wi-Fi discovery, mDNS) | BSD-3-Clause |
| `golang.org/x/net`, `golang.org/x/sys`, `golang.org/x/text` | BSD-3-Clause |

The Go standard library and runtime are BSD-3-Clause.

## Android app (`android/`) libraries

| Library | License |
|---------|---------|
| Bouncy Castle `bcprov-jdk18on` (Wi-Fi pairing SRP) | Bouncy Castle License (MIT style) |
| AndroidX `core-ktx`, `appcompat`; Material Components | Apache-2.0 |
| Kotlin standard library | Apache-2.0 |
