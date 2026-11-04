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
| `enigo` (mouse and key injection) | MIT |
| `evdev` (Linux uinput) | Apache-2.0 OR MIT |
| `ctrlc` (Linux signal handling) | MIT/Apache-2.0 |
| `windows` (Win32 bindings) | MIT OR Apache-2.0 |

`ffmpeg` is not bundled; it is installed separately and used as an external program.
