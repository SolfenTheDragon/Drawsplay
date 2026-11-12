# Security Policy

## Supported versions

Only the latest [release](https://github.com/SolfenTheDragon/Drawsplay/releases/latest) receives security fixes.

## Reporting a vulnerability

Please do not open a public issue for security problems.

Report privately through GitHub: [Report a vulnerability](https://github.com/SolfenTheDragon/Drawsplay/security/advisories/new).

Include the affected version, your OS and tablet, steps to reproduce, and the impact you see. You can expect an initial response within a few days. Fixes are released as a new version, and reporters are credited if they wish.

## Scope

DrawSplay's host reaches the tablet over USB as an Android Open Accessory (AOA) by default and opens no network port for it. Its other listeners are on localhost only:

- the settings page (`127.0.0.1:27180`), which answers only requests addressed to `localhost` or a loopback IP (a DNS-rebinding defence) and requires a per-session CSRF token for changes;
- the input and video ports for the optional `adb reverse` tunnel, which are open only while that setting is on and refuse connections from other users' processes.

The optional paired Wi-Fi transport (behind `DRAWSPLAY_FEATURE_WIFI`) discovers tablets on one chosen LAN interface and connects to their listeners, accepting only devices paired with a numeric code, over mutually authenticated TLS. The old token-based Wi-Fi mode has been removed.

Reports about these listeners, Wi-Fi pairing, the installer and uninstaller, the Linux udev rules and display helper (`pkexec`/polkit), and the bundled drivers (including the IddCx virtual display driver's control interface) are in scope.

DrawSplay Desktop (`desktop/`) is a separate component with its own listener: `dd-host` accepts connections on `127.0.0.1:47800` by default; LAN access requires an explicit `--bind` address. Connections are encrypted and mutually authenticated (Noise XX); only private-network addresses are served unless `--allow`/`--allow-any` says otherwise, a new device must be confirmed by comparing a ten-digit code on both machines (bound to commit-then-reveal nonces so it cannot be ground), and a paired client can inject keyboard, mouse, touch and pen input, so it is treated as fully trusted. Reports about its handshake, pairing, trust store or connection handling are in scope. See the DrawSplay Desktop section of `README.md` for how pairing works for users.
