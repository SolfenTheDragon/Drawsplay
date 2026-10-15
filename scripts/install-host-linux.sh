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
  command -v gst-inspect-1.0 >/dev/null 2>&1 || return 0
  if ! gst-inspect-1.0 "$1" >/dev/null 2>&1; then
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
  if command -v gst-inspect-1.0 >/dev/null 2>&1; then
    check_gst_element pipewiresrc "gstreamer1.0-pipewire"
    check_gst_element videoconvert "gstreamer1.0-plugins-base"
    check_gst_element videoscale "gstreamer1.0-plugins-base"
    check_gst_element videorate "gstreamer1.0-plugins-good"
    check_gst_element h264parse "gstreamer1.0-plugins-bad"
    check_gst_element vah264enc "gstreamer1.0-plugins-bad"
    check_gst_element vaapih264enc "gstreamer1.0-vaapi"
    check_gst_element x264enc "gstreamer1.0-plugins-ugly"
  fi
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

# --- User groups (uinput + adb USB access) ----------------------------------
user_groups="$(groups "$USER" 2>/dev/null || true)"
needs_groups=()
for g in input plugdev; do
  if [[ ! "$user_groups" =~ (^|[[:space:]])$g($|[[:space:]]) ]]; then
    needs_groups+=("$g")
  fi
done
if [[ ${#needs_groups[@]} -gt 0 ]]; then
  group_list="$(IFS=,; echo "${needs_groups[*]}")"
  echo "User '$USER' is missing group membership: ${needs_groups[*]} (needed for pen/uinput injection and USB access)."
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

# --- /dev/uinput permissions (pen/touch injection) --------------------------
UDEV_RULE_FILE="/etc/udev/rules.d/99-drawsplay-uinput.rules"
UDEV_RULE_LINE='KERNEL=="uinput", GROUP="input", MODE="0660"'
uinput_needs_fix=0
if [[ -e /dev/uinput ]]; then
  uinput_group="$(stat -c '%G' /dev/uinput 2>/dev/null || echo "")"
  uinput_mode="$(stat -c '%a' /dev/uinput 2>/dev/null || echo "")"
  if [[ "$uinput_group" != "input" || "$uinput_mode" != "660" ]]; then
    uinput_needs_fix=1
  fi
fi
if [[ ! -f "$UDEV_RULE_FILE" ]] || [[ "$uinput_needs_fix" -eq 1 ]]; then
  echo "/dev/uinput is not accessible to the 'input' group (needed for pen/touch injection)."
  if [[ "$SKIP_INSTALL" -eq 0 ]]; then
    if [[ ! -f "$UDEV_RULE_FILE" ]]; then
      echo "Installing udev rule: $UDEV_RULE_FILE"
      if echo "$UDEV_RULE_LINE" | sudo tee "$UDEV_RULE_FILE" >/dev/null; then
        sudo udevadm control --reload-rules 2>/dev/null || true
        sudo udevadm trigger --name-match=uinput 2>/dev/null || true
      else
        echo "Could not write udev rule. Run manually:"
        echo "  echo '$UDEV_RULE_LINE' | sudo tee $UDEV_RULE_FILE"
        echo "  sudo udevadm control --reload-rules && sudo udevadm trigger --name-match=uinput"
      fi
    fi
    sudo chgrp input /dev/uinput 2>/dev/null || true
    sudo chmod 0660 /dev/uinput 2>/dev/null || true
    if [[ "$(stat -c '%G:%a' /dev/uinput 2>/dev/null)" == "input:660" ]]; then
      echo "/dev/uinput permissions fixed."
    else
      echo "Could not verify /dev/uinput permissions; a reboot may be required."
      NEEDS_RESTART=1
    fi
  else
    echo "Run manually:"
    echo "  echo '$UDEV_RULE_LINE' | sudo tee $UDEV_RULE_FILE"
    echo "  sudo udevadm control --reload-rules && sudo udevadm trigger --name-match=uinput"
  fi
  echo
fi

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

# --- X11 extended display (vkms) --------------------------------------------
# X11 has no virtual-monitor API, so the host extends the desktop onto a vkms
# output. Without one the tablet mirrors the desktop. Loading vkms reconfigures
# the X screen, so it is only done after explicit confirmation.
x11_virtual_output() {
  xrandr --current 2>/dev/null | awk '
    ($1 ~ /^Virtual-[0-9]+-[0-9]+$/ && $2 == "connected") || $1 ~ /^VIRTUAL[0-9]+$/ { print $1; exit }'
}
vkms_load_configured() {
  grep -RqsE '^[[:space:]]*vkms([[:space:]]|$)' \
    /etc/modules-load.d /run/modules-load.d /usr/lib/modules-load.d /lib/modules-load.d 2>/dev/null
}
configure_vkms_autoload() {
  local modules_load_file="/etc/modules-load.d/drawsplay-vkms.conf"
  if [[ -d /sys/module/vkms/parameters ]] && ! grep -Rqs 'create_default_dev' /etc/modprobe.d 2>/dev/null; then
    printf 'options vkms create_default_dev=1\n' | sudo tee /etc/modprobe.d/drawsplay-vkms.conf >/dev/null || true
  fi
  if vkms_load_configured; then
    echo "vkms is already configured to load automatically at boot."
  elif printf 'vkms\n' | sudo tee -a "$modules_load_file" >/dev/null; then
    echo "Configured vkms to load automatically at boot."
  else
    echo "Could not configure vkms to load at boot."
    echo "Run manually: echo vkms | sudo tee $modules_load_file"
  fi
}
if [[ "$WAYLAND_CAPTURE" -eq 0 ]] && [[ -d /sys/module/vkms ]] && ! vkms_load_configured; then
  echo "vkms is loaded for this session but may not load after a restart."
  if [[ "$SKIP_INSTALL" -eq 0 && -t 0 ]]; then
    if read -r -p "Configure vkms to load automatically at boot? [Y/n] " reply; then
      if [[ -z "$reply" || "$reply" =~ ^[Yy] ]]; then
        configure_vkms_autoload
      fi
    fi
  else
    echo "To keep the extended display after restarts, run:"
    echo "  echo vkms | sudo tee /etc/modules-load.d/drawsplay-vkms.conf"
  fi
  echo
fi
if [[ "$WAYLAND_CAPTURE" -eq 0 ]] && [[ -z "${DRAWSPLAY_X11_OUTPUT:-}" ]] &&
  command -v xrandr >/dev/null 2>&1 && [[ -z "$(x11_virtual_output)" ]]; then
  if [[ -d /sys/module/vkms ]]; then
    echo "The vkms module is loaded, but the X server shows no virtual output for it,"
    echo "so the tablet will mirror the desktop. Log out and back in to reload the displays."
  elif PATH="$PATH:/usr/sbin:/sbin" modinfo vkms >/dev/null 2>&1; then
    echo "X11 session: the tablet mirrors the desktop unless a virtual display exists."
    echo "Loading vkms adds one, making the tablet a real second monitor."
    echo "The X server reconfigures its displays when vkms loads: save your work first."
    echo "If you agree, DrawSplay will also configure vkms to load automatically at boot."
    load_vkms=0
    reply=""
    if [[ "$SKIP_INSTALL" -eq 0 && -t 0 ]]; then
      if read -r -p "Load vkms now and at boot (sudo modprobe vkms)? Type 'yes' to confirm [y/N] " reply; then
        [[ "$reply" =~ ^[Yy]([Ee][Ss])?$ ]] && load_vkms=1
      fi
    else
      echo "To enable it now, run: sudo modprobe vkms"
      echo "To load it automatically at boot, run:"
      echo "  echo vkms | sudo tee /etc/modules-load.d/drawsplay-vkms.conf"
    fi
    if [[ "$load_vkms" -eq 1 ]]; then
      # Newer kernels create no virtual device unless create_default_dev=1; older
      # kernels reject that parameter, so fall back to a plain load.
      if sudo modprobe vkms create_default_dev=1 2>/dev/null || sudo modprobe vkms; then
        configure_vkms_autoload
        vkms_output=""
        for _ in 1 2 3 4 5 6 7 8 9 10; do
          vkms_output="$(x11_virtual_output)"
          [[ -n "$vkms_output" ]] && break
          sleep 0.5
        done
        if [[ -n "$vkms_output" ]]; then
          # The host switches the output on only while the tablet is connected.
          sleep 2
          xrandr --output "$vkms_output" --off 2>/dev/null || true
          echo "Virtual display $vkms_output is ready."
        else
          echo "vkms is loaded, but the X server shows no virtual output yet."
          echo "If the tablet still mirrors the desktop, log out and back in."
        fi
      else
        echo "Could not load vkms; the tablet will mirror the desktop."
      fi
    fi
  else
    echo "This kernel has no vkms module; the tablet will mirror the desktop on X11."
  fi
  echo
fi

echo "DrawSplay maps 'Drawsplay Pen' to the tablet's display itself."
if [[ "$NEEDS_RESTART" -eq 1 ]]; then
  echo "Restart required: system drivers or permissions changed. Restart your computer before using DrawSplay."
elif [[ "$NEEDS_RELOGIN" -eq 1 ]]; then
  echo "Log out required: your user was added to new groups. Log out and back in before using DrawSplay."
fi
echo "Installation complete. Start DrawSplay from the applications menu or desktop shortcut."
