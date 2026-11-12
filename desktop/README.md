# DrawSplay Desktop

Use one Windows or Linux PC as another PC's display:

- `dd-host` runs on the computer whose desktop you want to extend.
- `dd-client` runs on the computer that becomes the second screen.

Install `ffmpeg` on both computers. Linux graphical dialogs also need `zenity`;
without it, run from a terminal to answer connection and pairing prompts.

On the host:

```sh
dd-host --bind YOUR_LAN_IP:47800
```

On the client:

```sh
dd-client
```

Compare the pairing codes on both computers before accepting. A trusted client can
type and click as you on the host. A bare host listens only on loopback; virtual
display creation may fall back to mirroring the real screen. Use
`--require-virtual` to refuse that fallback.

See the [desktop guide](GUIDE.md) for installation, display backends, pairing,
builds, and safely isolated tests. `--help` lists each program's options.

Release archives contain the GPL-3.0 license in `LICENSE`, third-party notices in
`THIRD-PARTY-NOTICES.md`, and the driver's license at
`drivers/windows/idd/LICENSE`. FFmpeg and the Windows virtual display driver
are installed separately, not bundled in these desktop archives.

Project source and issue tracking:
<https://github.com/Solfen/Drawsplay>.
