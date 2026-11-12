#!/usr/bin/env bash
# Installs the DrawSplay host (Linux): binary, launchers, login service,
# runtime dependencies and USB/input permissions. Run as your normal user, not
# with sudo; sudo is requested only for the steps that need it.
# Usage: install-host-linux.sh [--no-install-deps] [path/to/drawsplay-host-linux-amd64]
set -euo pipefail

if [[ "$(id -u)" -eq 0 ]]; then
  echo "Run this script as your normal user, not with sudo." >&2
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKIP_INSTALL=0
SOURCE_BIN=""
for arg in "$@"; do
  if [[ "$arg" == "--no-install-deps" ]]; then
    SKIP_INSTALL=1
  elif [[ -z "$SOURCE_BIN" ]]; then
    SOURCE_BIN="$arg"
  fi
done
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
# Set when this run changed something that needs a new login session or a reboot.
NEEDS_RELOGIN=0
NEEDS_RESTART=0

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

# --- Dependency detection ---------------------------------------------------
# ADB is only needed when the optional adb tunnel is enabled (env override or saved setting).
ADB_MODE=0
if [[ "${DRAWSPLAY_ADB_TUNNEL:-}" == "1" ]] ||
  grep -qs '"adbTunnelEnabled": *true' "${XDG_CONFIG_HOME:-$HOME/.config}"/DrawSplay/*.json; then
  ADB_MODE=1
fi

WAYLAND_CAPTURE=1
if [[ "${XDG_SESSION_TYPE:-}" == "x11" ]] || {
  [[ -z "${XDG_SESSION_TYPE:-}" ]] &&
    [[ -n "${DISPLAY:-}" ]] &&
    [[ -z "${WAYLAND_DISPLAY:-}" ]]
}; then
  WAYLAND_CAPTURE=0
fi

missing_pkgs=()
add_pkgs() {
  local p
  for p in $1; do
    missing_pkgs+=("$p")
  done
}
check_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing command: $1 (needs: $2)"
    add_pkgs "$2"
  fi
}
check_gst_element() {
  if ! command -v gst-inspect-1.0 >/dev/null 2>&1 ||
    ! gst-inspect-1.0 "$1" >/dev/null 2>&1; then
    echo "Missing GStreamer element: $1 (needs: $2)"
    add_pkgs "$2"
  fi
}

check_cmd ffmpeg "ffmpeg"
check_cmd vainfo "vainfo"
if [[ "$ADB_MODE" -eq 1 ]]; then
  check_cmd adb "android-sdk-platform-tools android-sdk-platform-tools-common"
fi
if [[ "$WAYLAND_CAPTURE" -eq 1 ]]; then
  check_cmd gst-launch-1.0 "gstreamer1.0-tools"
  check_cmd gst-inspect-1.0 "gstreamer1.0-tools"
  check_gst_element pipewiresrc "gstreamer1.0-pipewire"
  check_gst_element videoconvert "gstreamer1.0-plugins-base"
  check_gst_element videoscale "gstreamer1.0-plugins-base"
  check_gst_element videorate "gstreamer1.0-plugins-good"
  check_gst_element h264parse "gstreamer1.0-plugins-bad"
  check_gst_element vah264enc "gstreamer1.0-plugins-bad"
  check_gst_element vaapih264enc "gstreamer1.0-vaapi"
  check_gst_element x264enc "gstreamer1.0-plugins-ugly"
else
  check_cmd xrandr "x11-xserver-utils"
fi

if [[ ${#missing_pkgs[@]} -gt 0 ]]; then
  deduped=()
  for p in "${missing_pkgs[@]}"; do
    found=0
    for d in "${deduped[@]:-}"; do
      [[ "$d" == "$p" ]] && found=1 && break
    done
    [[ $found -eq 0 ]] && deduped+=("$p")
  done
  missing_pkgs=("${deduped[@]}")

  echo
  echo "Missing packages: ${missing_pkgs[*]}"
  if [[ "$SKIP_INSTALL" -eq 1 ]]; then
    echo "Skipping auto-install (--no-install-deps given). Install manually with:"
    echo "  sudo apt install ${missing_pkgs[*]}"
  elif ! command -v apt-get >/dev/null 2>&1; then
    echo "apt-get not found; install these packages manually with your package manager:"
    echo "  ${missing_pkgs[*]}"
  else
    echo "Installing missing packages (you may be prompted for your sudo password)..."
    if sudo apt-get update && sudo apt-get install -y "${missing_pkgs[@]}"; then
      echo "Dependencies installed successfully."
    else
      echo "Automatic install failed. Install manually with:"
      echo "  sudo apt install ${missing_pkgs[*]}"
    fi
  fi
  echo
fi

# --- VA-API GPU driver ------------------------------------------------------
if command -v vainfo >/dev/null 2>&1 && ! vainfo 2>/dev/null | grep -q "EncSlice"; then
  echo "No VA-API encode-capable driver detected; capture will fall back to slower software x264 encoding."
  driver_pkg=""
  if command -v lspci >/dev/null 2>&1; then
    gpu_info="$(lspci 2>/dev/null | grep -iE 'vga|3d|display' || true)"
    if echo "$gpu_info" | grep -qi intel; then
      driver_pkg="intel-media-va-driver-non-free"
    elif echo "$gpu_info" | grep -qiE 'amd|radeon'; then
      driver_pkg="mesa-va-drivers"
    fi
  fi
  if [[ -n "$driver_pkg" ]]; then
    if [[ "$SKIP_INSTALL" -eq 0 ]] && command -v apt-get >/dev/null 2>&1; then
      echo "Installing GPU VA-API driver package: $driver_pkg (you may be prompted for your sudo password)..."
      if sudo apt-get update && sudo apt-get install -y "$driver_pkg"; then
        NEEDS_RESTART=1
      else
        echo "Automatic install failed. Install manually with: sudo apt install $driver_pkg"
      fi
    else
      echo "Install manually with: sudo apt install $driver_pkg"
    fi
  else
    echo "Could not auto-detect GPU vendor; install one of:"
    echo "  sudo apt install intel-media-va-driver-non-free   # Intel integrated graphics"
    echo "  sudo apt install mesa-va-drivers                  # AMD graphics"
  fi
  echo
fi

# --- User groups (USB access, spare HDMI port) ------------------------------
# plugdev lets the host force a spare HDMI port on (70-drawsplay-drm-connector.rules).
# /dev/uinput needs no group: 99-drawsplay-uinput.rules grants it to the
# logged-in session. The "input" group is not used: it can read every keyboard.
user_groups="$(groups "$USER" 2>/dev/null || true)"
needs_groups=()
for g in plugdev; do
  if [[ ! "$user_groups" =~ (^|[[:space:]])$g($|[[:space:]]) ]]; then
    needs_groups+=("$g")
  fi
done
if [[ ${#needs_groups[@]} -gt 0 ]]; then
  group_list="$(IFS=,; echo "${needs_groups[*]}")"
  echo "User '$USER' is missing group membership: ${needs_groups[*]} (needed for USB and display access)."
  if [[ "$SKIP_INSTALL" -eq 0 ]]; then
    if sudo usermod -aG "$group_list" "$USER"; then
      echo "Added $USER to: ${needs_groups[*]}. Log out and back in for this to take effect."
      NEEDS_RELOGIN=1
    else
      echo "Could not add groups automatically. Run manually: sudo usermod -aG $group_list $USER"
    fi
  else
    echo "Run manually: sudo usermod -aG $group_list $USER"
  fi
  echo
fi

# --- /dev/uinput access (pen, touch and keyboard injection) -----------------
# Shared with DrawSplay Desktop's installer, which installs the same file.
UINPUT_RULE_SOURCE="$SCRIPT_DIR/99-drawsplay-uinput.rules"
if [[ ! -f "$UINPUT_RULE_SOURCE" ]]; then
  UINPUT_RULE_SOURCE="$ROOT_DIR/packaging/linux/99-drawsplay-uinput.rules"
fi
UINPUT_RULE_TARGET="/etc/udev/rules.d/99-drawsplay-uinput.rules"
UINPUT_MANUAL="  sudo install -m 0644 99-drawsplay-uinput.rules /etc/udev/rules.d/ && sudo udevadm control --reload && sudo udevadm trigger --name-match=uinput"
if [[ ! -f "$UINPUT_RULE_SOURCE" ]]; then
  echo "udev rule 99-drawsplay-uinput.rules not found; pen and touch input need it. Download it and run:"
  echo "$UINPUT_MANUAL"
elif cmp -s "$UINPUT_RULE_SOURCE" "$UINPUT_RULE_TARGET" 2>/dev/null; then
  echo "uinput udev rule already installed: $UINPUT_RULE_TARGET"
elif [[ "$SKIP_INSTALL" -eq 1 ]]; then
  echo "Run manually:"
  echo "$UINPUT_MANUAL"
else
  # Replaces the older rule, which gave /dev/uinput to the "input" group.
  echo "Installing uinput udev rule for pen and touch input (asks for your password)..."
  if sudo install -m 0644 "$UINPUT_RULE_SOURCE" "$UINPUT_RULE_TARGET" &&
    sudo udevadm control --reload-rules && sudo udevadm trigger --name-match=uinput; then
    echo "Installed $UINPUT_RULE_TARGET."
    if [[ ! -w /dev/uinput ]]; then
      echo "/dev/uinput is not writable yet; log out and back in."
      NEEDS_RELOGIN=1
    fi
  else
    echo "Could not install the udev rule. Run manually:" >&2
    echo "$UINPUT_MANUAL" >&2
  fi
fi
echo

# --- Android USB access (accessory mode) ------------------------------------
UDEV_RULE_SOURCE="$SCRIPT_DIR/70-drawsplay-aoa.rules"
if [[ ! -f "$UDEV_RULE_SOURCE" ]]; then
  UDEV_RULE_SOURCE="$ROOT_DIR/packaging/linux/70-drawsplay-aoa.rules"
fi
AOA_RULE_TARGET="/etc/udev/rules.d/70-drawsplay-aoa.rules"
UDEV_MANUAL="  sudo cp 70-drawsplay-aoa.rules /etc/udev/rules.d/ && sudo udevadm control --reload && sudo udevadm trigger"
if [[ ! -f "$UDEV_RULE_SOURCE" ]]; then
  echo "udev rule 70-drawsplay-aoa.rules not found; USB accessory mode needs it. Download it and run:"
  echo "$UDEV_MANUAL"
elif cmp -s "$UDEV_RULE_SOURCE" "$AOA_RULE_TARGET" 2>/dev/null; then
  echo "USB udev rule already installed: $AOA_RULE_TARGET"
elif [[ "$SKIP_INSTALL" -eq 1 ]]; then
  echo "Run manually:"
  echo "$UDEV_MANUAL"
else
  echo "Installing USB udev rule so your user can open the tablet (asks for your password)..."
  if sudo install -m 0644 "$UDEV_RULE_SOURCE" "$AOA_RULE_TARGET" &&
    sudo udevadm control --reload-rules && sudo udevadm trigger --subsystem-match=usb; then
    echo "Installed $AOA_RULE_TARGET. Replug the tablet."
  else
    echo "Could not install the udev rule. Run manually:" >&2
    echo "$UDEV_MANUAL" >&2
  fi
fi
echo

# --- Display helper (X11 virtual display, run through pkexec) ---------------
# The host switches spare HDMI ports and loads vkms through this small,
# fixed-purpose helper, so the password prompt names a DrawSplay action
# instead of asking to run a shell as root. Installed whatever the current
# session type: the user may log into X11 later, and the X11 fallback must be
# ready then without another install.
{
  HELPER_SOURCE="$SCRIPT_DIR/drawsplay-display-helper"
  POLICY_SOURCE="$SCRIPT_DIR/com.drawsplay.display-helper.policy"
  if [[ ! -f "$HELPER_SOURCE" ]]; then
    HELPER_SOURCE="$ROOT_DIR/packaging/linux/drawsplay-display-helper"
    POLICY_SOURCE="$ROOT_DIR/packaging/linux/com.drawsplay.display-helper.policy"
  fi
  HELPER_TARGET="/usr/libexec/drawsplay/drawsplay-display-helper"
  POLICY_TARGET="/usr/share/polkit-1/actions/com.drawsplay.display-helper.policy"
  if [[ ! -f "$HELPER_SOURCE" || ! -f "$POLICY_SOURCE" ]]; then
    echo "Display helper files not found; the host will fall back to a generic administrator prompt."
  elif cmp -s "$HELPER_SOURCE" "$HELPER_TARGET" 2>/dev/null && cmp -s "$POLICY_SOURCE" "$POLICY_TARGET" 2>/dev/null; then
    echo "Display helper already installed: $HELPER_TARGET"
  elif [[ "$SKIP_INSTALL" -eq 1 ]]; then
    echo "Optional: install the display helper so the password prompt names DrawSplay:"
    echo "  sudo install -D -o root -g root -m 0755 drawsplay-display-helper $HELPER_TARGET"
    echo "  sudo install -D -o root -g root -m 0644 com.drawsplay.display-helper.policy $POLICY_TARGET"
  else
    echo "Installing the display helper (asks for your password)..."
    if sudo install -D -o root -g root -m 0755 "$HELPER_SOURCE" "$HELPER_TARGET" &&
      sudo install -D -o root -g root -m 0644 "$POLICY_SOURCE" "$POLICY_TARGET"; then
      echo "Installed $HELPER_TARGET"
    else
      echo "Could not install the display helper; the host falls back to a generic administrator prompt." >&2
    fi
  fi
}
echo

# --- Spare HDMI connector access (X11 virtual display fallback) --------------
# Lets the plugdev group force an unplugged HDMI port on, so the X11 fallback
# virtual display works without root or a password prompt on every boot. This
# is what keeps the tablet extending the desktop on kernels where vkms is
# unsafe, so it is installed on every session type (see the display helper).
{
  DRM_RULE_SOURCE="$SCRIPT_DIR/70-drawsplay-drm-connector.rules"
  if [[ ! -f "$DRM_RULE_SOURCE" ]]; then
    DRM_RULE_SOURCE="$ROOT_DIR/packaging/linux/70-drawsplay-drm-connector.rules"
  fi
  DRM_RULE_TARGET="/etc/udev/rules.d/70-drawsplay-drm-connector.rules"
  DRM_MANUAL="  sudo cp 70-drawsplay-drm-connector.rules /etc/udev/rules.d/ && sudo udevadm control --reload && sudo udevadm trigger --subsystem-match=drm --action=change"
  if [[ ! -f "$DRM_RULE_SOURCE" ]]; then
    echo "udev rule 70-drawsplay-drm-connector.rules not found; the X11 spare-connector display will ask for a password each boot without it."
  elif cmp -s "$DRM_RULE_SOURCE" "$DRM_RULE_TARGET" 2>/dev/null; then
    echo "Display connector udev rule already installed: $DRM_RULE_TARGET"
  elif [[ "$SKIP_INSTALL" -eq 1 ]]; then
    echo "Run manually:"
    echo "$DRM_MANUAL"
  else
    echo "Installing display connector udev rule (asks for your password)..."
    if sudo install -m 0644 "$DRM_RULE_SOURCE" "$DRM_RULE_TARGET" &&
      sudo udevadm control --reload-rules && sudo udevadm trigger --subsystem-match=drm --action=change; then
      echo "Installed $DRM_RULE_TARGET."
    else
      echo "Could not install the udev rule. Run manually:" >&2
      echo "$DRM_MANUAL" >&2
    fi
  fi
  echo
}

# --- X11 extended display ---------------------------------------------------
# On X11 the host extends the desktop onto a vkms output, falling back to a
# spare, unplugged HDMI connector of the GPU (forced on at runtime, nothing
# persists across boots), and mirrors the desktop when there is neither. The
# host loads vkms itself and guards it; the installer never loads or autoloads
# it, because on some kernels (for example Linux 7.0 OEM) the X server fails on
# it, which took the display down.
if [[ "$WAYLAND_CAPTURE" -eq 0 ]]; then
  for vkms_conf in /etc/modules-load.d/drawsplay-vkms.conf /etc/modprobe.d/drawsplay-vkms.conf; do
    if [[ -e "$vkms_conf" ]]; then
      echo "An earlier DrawSplay install configured vkms to load at boot ($vkms_conf)."
      echo "That can crash the display on some kernels. To remove it, run:"
      echo "  sudo rm /etc/modules-load.d/drawsplay-vkms.conf /etc/modprobe.d/drawsplay-vkms.conf && sudo modprobe -r vkms"
      break
    fi
  done
  echo
fi

echo "DrawSplay maps 'Drawsplay Pen' to the tablet's display itself."
if [[ "$NEEDS_RESTART" -eq 1 ]]; then
  echo "Restart required: system drivers or permissions changed. Restart your computer before using DrawSplay."
elif [[ "$NEEDS_RELOGIN" -eq 1 ]]; then
  echo "Log out required: your user was added to new groups. Log out and back in before using DrawSplay."
fi
echo "Installation complete. Start DrawSplay from the applications menu or desktop shortcut."
