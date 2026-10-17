# Security Policy

## Supported versions

Only the latest [release](https://github.com/SolfenTheDragon/Drawsplay/releases/latest) receives security fixes.

## Reporting a vulnerability

Please do not open a public issue for security problems.

Report privately through GitHub: [Report a vulnerability](https://github.com/SolfenTheDragon/Drawsplay/security/advisories/new).

Include the affected version, your OS and tablet, steps to reproduce, and the impact you see. You can expect an initial response within a few days. Fixes are released as a new version, and reporters are credited if they wish.

## Scope

DrawSplay's host listens only on localhost (input, video and the settings page) and reaches the tablet over USB via `adb reverse`. Reports about these listeners, the optional Wi-Fi mode (TLS and token authentication), the installer and the bundled drivers are in scope.
