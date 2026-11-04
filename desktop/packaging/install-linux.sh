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
    sudo cp "$here/99-drawsplay-desktop.rules" /etc/udev/rules.d/
    sudo udevadm control --reload
    sudo udevadm trigger
    sudo usermod -aG input "$USER"
    echo "udev rule installed. Log out and back in for the group change to apply."
else
    echo "For touch, pen and full keyboard input on a Linux host, re-run with --udev."
fi
