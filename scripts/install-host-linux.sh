#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_BIN="${1:-}"
if [[ -z "$SOURCE_BIN" ]]; then
  if [[ -f "$SCRIPT_DIR/drawsplay-host-linux-amd64" ]]; then
    SOURCE_BIN="$SCRIPT_DIR/drawsplay-host-linux-amd64"
  else
    SOURCE_BIN="$ROOT_DIR/host/host"
  fi
fi
ICON_SOURCE="$SCRIPT_DIR/drawsplay-tray.png"
if [[ ! -f "$ICON_SOURCE" ]]; then
  ICON_SOURCE="$ROOT_DIR/host/drawsplay-tray.png"
fi
INSTALL_DIR="${HOME}/.local/bin"
TARGET_BIN="${INSTALL_DIR}/drawsplay-host"
ICON_DIR="${HOME}/.local/share/icons/hicolor/64x64/apps"
ICON_TARGET="${ICON_DIR}/drawsplay-host.png"
APPLICATIONS_DIR="${HOME}/.local/share/applications"
DESKTOP_ENTRY="${APPLICATIONS_DIR}/drawsplay-host.desktop"

desktop_exec_arg() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//\$/\\$}"
  value="${value//\`/\\\`}"
  printf '"%s"' "$value"
}

if [[ ! -f "$SOURCE_BIN" ]]; then
  echo "Host binary not found: $SOURCE_BIN" >&2
  exit 1
fi
if [[ ! -f "$ICON_SOURCE" ]]; then
  echo "DrawSplay icon not found: $ICON_SOURCE" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR" "$ICON_DIR" "$APPLICATIONS_DIR"
install -m 0755 "$SOURCE_BIN" "$TARGET_BIN"
echo "Installed host to $TARGET_BIN"
install -m 0644 "$ICON_SOURCE" "$ICON_TARGET"
printf '[Desktop Entry]\nType=Application\nName=DrawSplay\nComment=Use your Android tablet as an extended display\nExec=%s\nIcon=%s\nTerminal=false\nCategories=Graphics;\nStartupNotify=false\n' \
  "$(desktop_exec_arg "$TARGET_BIN")" "$ICON_TARGET" > "$DESKTOP_ENTRY"
chmod 0755 "$DESKTOP_ENTRY"
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database "$APPLICATIONS_DIR"
fi

DESKTOP_DIR=""
if command -v xdg-user-dir >/dev/null 2>&1; then
  DESKTOP_DIR="$(xdg-user-dir DESKTOP)"
fi
if [[ -z "$DESKTOP_DIR" ]]; then
  DESKTOP_DIR="${HOME}/Desktop"
fi
mkdir -p "$DESKTOP_DIR"
DESKTOP_SHORTCUT="${DESKTOP_DIR}/drawsplay-host.desktop"
install -m 0755 "$DESKTOP_ENTRY" "$DESKTOP_SHORTCUT"
if command -v gio >/dev/null 2>&1; then
  if ! gio set "$DESKTOP_SHORTCUT" metadata::trusted true; then
    echo "Could not mark the desktop shortcut as trusted; use its context menu to allow launching." >&2
  fi
else
  echo "Mark the desktop shortcut as trusted in its context menu before launching it."
fi
echo "Created desktop shortcut at $DESKTOP_SHORTCUT"
echo "Created applications-menu entry at $DESKTOP_ENTRY"

"$TARGET_BIN" -service install
echo

# ADB is only needed when the optional adb tunnel is enabled (env override or saved setting).
ADB_MODE=0
if [[ "${DRAWSPLAY_ADB_TUNNEL:-}" == "1" ]] ||
  grep -qs '"adbTunnelEnabled": *true' "${XDG_CONFIG_HOME:-$HOME/.config}"/DrawSplay/*.json; then
  ADB_MODE=1
fi

missing=()
commands=(ffmpeg)
if [[ "$ADB_MODE" -eq 1 ]]; then
  commands+=(adb)
fi
WAYLAND_CAPTURE=1
if [[ "${XDG_SESSION_TYPE:-}" == "x11" ]] || {
  [[ -z "${XDG_SESSION_TYPE:-}" ]] &&
    [[ -n "${DISPLAY:-}" ]] &&
    [[ -z "${WAYLAND_DISPLAY:-}" ]]
}; then
  WAYLAND_CAPTURE=0
fi
if [[ "$WAYLAND_CAPTURE" -eq 1 ]]; then
  commands+=(gst-launch-1.0 gst-inspect-1.0)
else
  commands+=(xrandr)
fi
for cmd in "${commands[@]}"; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    missing+=("$cmd")
  fi
done

if [[ ${#missing[@]} -gt 0 ]]; then
  echo "Missing Linux runtime tools: ${missing[*]}"
  echo "Install the missing tools with your package manager. On Ubuntu/Debian:"
  if [[ "$ADB_MODE" -eq 1 ]]; then
    echo "  sudo apt install android-sdk-platform-tools android-sdk-platform-tools-common ffmpeg"
  else
    echo "  sudo apt install ffmpeg"
  fi
  if [[ "$WAYLAND_CAPTURE" -eq 1 ]]; then
    echo "  sudo apt install gstreamer1.0-tools gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-pipewire"
  else
    echo "  sudo apt install x11-xserver-utils"
  fi
  echo
fi

if [[ "$WAYLAND_CAPTURE" -eq 0 ]] && [[ ! -d /sys/module/vkms ]]; then
  echo "X11 extended display: load the vkms kernel module so the tablet becomes a second monitor"
  echo "instead of mirroring the desktop (save your work first; this reconfigures X11):"
  echo "  sudo modprobe vkms"
  echo "To load it automatically at boot:"
  echo "  echo vkms | sudo tee /etc/modules-load.d/drawsplay-vkms.conf"
  echo
fi

if command -v vainfo >/dev/null 2>&1; then
  if ! vainfo 2>/dev/null | grep -q "EncSlice"; then
    echo "No VA-API encode-capable driver detected; capture will fall back to software x264 (lower quality/throughput)."
    echo "  sudo apt install intel-media-va-driver-non-free   # Intel integrated graphics"
    echo "  sudo apt install mesa-va-drivers                  # AMD graphics"
    echo
  fi
else
  echo "vainfo not found; install a VA-API driver for GPU-accelerated encoding (recommended):"
  echo "  sudo apt install intel-media-va-driver-non-free vainfo   # Intel integrated graphics"
  echo "  sudo apt install mesa-va-drivers vainfo                  # AMD graphics"
  echo
fi

if [[ "$ADB_MODE" -eq 1 ]]; then
  USB_NOTE="uinput + adb USB access"
else
  USB_NOTE="uinput access"
fi
user_groups="$(groups "$USER" 2>/dev/null || true)"
if [[ ! "$user_groups" =~ (^|[[:space:]])input($|[[:space:]]) ]] || [[ ! "$user_groups" =~ (^|[[:space:]])plugdev($|[[:space:]]) ]]; then
  echo "User groups note: add your user to input and plugdev for $USB_NOTE:"
  echo "  sudo usermod -aG input,plugdev $USER"
  echo "Then log out and back in."
  echo
fi

UDEV_RULE_SOURCE="$SCRIPT_DIR/70-drawsplay-aoa.rules"
if [[ ! -f "$UDEV_RULE_SOURCE" ]]; then
  UDEV_RULE_SOURCE="$ROOT_DIR/packaging/linux/70-drawsplay-aoa.rules"
fi
UDEV_RULE_TARGET="/etc/udev/rules.d/70-drawsplay-aoa.rules"
UDEV_MANUAL="  sudo cp 70-drawsplay-aoa.rules /etc/udev/rules.d/ && sudo udevadm control --reload && sudo udevadm trigger"
# USB accessory mode (default, no Developer options) needs this rule so the user can open the tablet.
if [[ ! -f "$UDEV_RULE_SOURCE" ]]; then
  echo "udev rule 70-drawsplay-aoa.rules not found; USB accessory mode needs it. Download it and run:"
  echo "$UDEV_MANUAL"
elif cmp -s "$UDEV_RULE_SOURCE" "$UDEV_RULE_TARGET" 2>/dev/null; then
  echo "USB udev rule already installed: $UDEV_RULE_TARGET"
else
  echo "Installing USB udev rule so your user can open the tablet (asks for your password)..."
  if sudo install -m 0644 "$UDEV_RULE_SOURCE" "$UDEV_RULE_TARGET" &&
    sudo udevadm control --reload-rules && sudo udevadm trigger --subsystem-match=usb; then
    echo "Installed $UDEV_RULE_TARGET. Replug the tablet."
  else
    echo "Could not install the udev rule. Run manually:" >&2
    echo "$UDEV_MANUAL" >&2
  fi
fi
echo

echo "If pen injection is disabled, grant /dev/uinput access (MODE=0660 GROUP=input). DrawSplay maps 'Drawsplay Pen' to the tablet's display itself."
