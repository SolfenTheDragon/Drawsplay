# DrawSplay Desktop guide

## Installation and requirements

Install `ffmpeg` on both PCs and put it on `PATH`. On Linux, install `zenity` for
graphical connection, pairing, and trusted-device dialogs. If zenity is missing,
start the programs in a terminal; `--terminal` explicitly selects terminal prompts.

Linux release archives include `install-linux.sh`. Run it to install the two
binaries in `~/.local/bin`, or run `./install-linux.sh --udev` to also install the
shared `/dev/uinput` rule (uses sudo). Log out and back in if input remains
unavailable. The installer does not install ffmpeg, zenity, or compositor tools.

Windows users can launch the executables from the extracted archive or a terminal.
A Windows host needs the separately installed DrawSplay IddCx virtual display
driver for an extra monitor. Driver installation is provided by the main
DrawSplay installer, not these desktop packages.

Linux display backends:

- X11: `xrandr` and an unused output that the GPU can enable. Some GPUs require an
  HDMI dummy plug or a preconfigured virtual output.
- sway/Hyprland: `wf-recorder`; creating headless outputs also requires compositor
  support (sway generally needs its nested/headless backend).
- GNOME/KDE and other Wayland desktops: a working desktop portal, PipeWire,
  `gstreamer1.0-tools`, and `gstreamer1.0-pipewire`. The portal asks for permission.

Without a virtual display the host may mirror an existing screen. Use
`--require-virtual` to prevent this. Pen pressure, tilt, and buttons require a
Windows client; a Linux client reports a pen as a mouse. There is no audio.

## Connections and pairing

Start `dd-host --bind YOUR_LAN_IP:47800` on the computer to extend, permit the port
through its firewall, then run `dd-client` on the other computer. A bare host
listens on `127.0.0.1:47800` only. By default, the peer-address filter allows
private, loopback, link-local, and carrier-grade NAT networks; `--allow NET`
adds a network.

The client launcher offers Connect, Trusted devices, and Quit. For command-line
use, run `dd-client HOST_IP --fullscreen --fit`. `--size 1920x1080` selects the
requested stream size; it does not cancel an explicit `--fullscreen`.
`Ctrl+Alt+Shift+Q` exits an active client.

Approve pairing only when both screens display the same ten-digit code. Questions
expire after two minutes; terminal prompts accept `y` or `n`. For unattended
deployment, exchange identity keys using `--show-key` and `--trust KEY` on both
computers. `--manage-trusted`, `--list-trusted`, and `--forget KEY_PREFIX` manage
saved trust; removing an active peer's trust disconnects it.

`--max-clients N` permits multiple clients, each with its own virtual output when
available. Linux absolute input follows desktop topology changes. A lost
connection disables input and clears the stale picture while the client retries.

## Building

Use a current stable Rust toolchain. On Linux, build prerequisites include
`pkg-config libx11-dev libxkbcommon-dev libwayland-dev libxcursor-dev libxi-dev
libxrandr-dev libxdo-dev libxtst-dev libudev-dev`. Install ffmpeg for video tests;
zenity is a runtime GUI dependency, not a Rust build dependency.

From the repository's `desktop/` directory:

```sh
cargo build --release --locked
cargo fmt --check
cargo clippy --all-targets --locked -- -D warnings
sh packaging/package-linux.sh
# Windows:
packaging/package-windows.ps1
```

Both packaging scripts include this guide, the desktop README, and license
notices. Linux cross-builds accept `TARGET=aarch64-unknown-linux-gnu`; Windows
accepts `-Target aarch64-pc-windows-msvc`. Cross-builds require the target's
toolchain and native libraries.

## Safe automated tests

The host's `--source test` generates synthetic video without opening, querying,
or cleaning up real displays, sharing a desktop, or creating input devices.
Input messages are ignored in this mode. Loopback integration tests scrub
desktop session variables and use isolated configuration folders.

To keep all test scratch files inside the checkout on Linux:

```sh
mkdir -p target/test-runtime
TMPDIR="$PWD/target/test-runtime" cargo test --workspace --locked
```

The normal suite ignores tests requiring a real PipeWire service or an X server.
The X11 scroll test requires preinstalled Xvfb, xauth, xdotool, and ffmpeg. Never
point it at a user's display; it checks that its display belongs to Xvfb:

```sh
env -u WAYLAND_DISPLAY -u SWAYSOCK -u HYPRLAND_INSTANCE_SIGNATURE \
  -u DRAWSPLAY_DESKTOP_CONFIG \
  TMPDIR="$PWD/target/test-runtime" \
  xvfb-run -a env DD_TEST_X11_SCROLL=1 \
  cargo test --locked -p dd-client --test x11_scroll -- --ignored
```

Do not run the ignored real-PipeWire test on a shared desktop as part of routine
validation. It deliberately publishes and captures a video node.
