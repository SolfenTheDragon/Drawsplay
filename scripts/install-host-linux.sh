#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE_BIN="${1:-$ROOT_DIR/host/host}"
INSTALL_DIR="${HOME}/.local/bin"
TARGET_BIN="${INSTALL_DIR}/drawsplay-host"

if [[ ! -f "$SOURCE_BIN" ]]; then
  echo "Host binary not found: $SOURCE_BIN" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
install -m 0755 "$SOURCE_BIN" "$TARGET_BIN"
echo "Installed host to $TARGET_BIN"

"$TARGET_BIN" -service install
echo

missing=()
for cmd in adb ffmpeg gst-launch-1.0 gst-inspect-1.0; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    missing+=("$cmd")
  fi
done

if [[ ${#missing[@]} -gt 0 ]]; then
  echo "Missing Linux runtime tools: ${missing[*]}"
  echo "Ubuntu install command:"
  echo "  sudo apt install android-sdk-platform-tools android-sdk-platform-tools-common ffmpeg gstreamer1.0-tools gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-pipewire"
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

user_groups="$(groups "$USER" 2>/dev/null || true)"
if [[ ! "$user_groups" =~ (^|[[:space:]])input($|[[:space:]]) ]] || [[ ! "$user_groups" =~ (^|[[:space:]])plugdev($|[[:space:]]) ]]; then
  echo "User groups note: add your user to input and plugdev for uinput + adb USB access:"
  echo "  sudo usermod -aG input,plugdev $USER"
  echo "Then log out and back in."
  echo
fi

echo "If pen injection is disabled, grant /dev/uinput access (MODE=0660 GROUP=input) and map 'Drawsplay Pen' in GNOME Wacom Tablet settings."
