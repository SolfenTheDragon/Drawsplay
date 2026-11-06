#!/bin/sh
# Installs dd-host and dd-client for the current user (~/.local/bin).
# Run with --udev to also install the /dev/uinput rule (asks for sudo).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
dest="${HOME}/.local/bin"
mkdir -p "$dest"
install -m 0755 "$here/dd-host" "$here/dd-client" "$dest/"
echo "Installed dd-host and dd-client to $dest"
case ":$PATH:" in *":$dest:"*) ;; *) echo "Note: $dest is not on your PATH." ;; esac
command -v ffmpeg >/dev/null 2>&1 || echo "Warning: ffmpeg is not installed (needed by both programs)."
if [ "${1:-}" = "--udev" ]; then
    # The DrawSplay host's installer installs the same rule under the same name.
    sudo install -m 0644 "$here/99-drawsplay-uinput.rules" /etc/udev/rules.d/
    # Older DrawSplay Desktop releases installed the same rule under another name.
    sudo rm -f /etc/udev/rules.d/99-drawsplay-desktop.rules
    sudo udevadm control --reload
    sudo udevadm trigger --name-match=uinput
    echo "udev rule installed. It gives the user logged in at this computer access to /dev/uinput;"
    echo "if input does not work right away, log out and back in."
else
    echo "For touch, pen and full keyboard input on a Linux host, re-run with --udev."
fi
