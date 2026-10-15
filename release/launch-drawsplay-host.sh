#!/usr/bin/env bash
# Launches the DrawSplay host service (Linux) from the release folder.
# Checks runtime dependencies and, if missing, offers to install them via apt.
# GStreamer/PipeWire is checked only for Wayland capture sessions; on X11 the
# script offers to load the vkms kernel module and configure it for boot.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOST_BIN="$SCRIPT_DIR/drawsplay-host-linux-amd64"
SKIP_INSTALL=0
HOST_ARGS=()
# Tracks whether *this run* actually applied a fix that needs a new login
# session or a reboot to fully take effect, so the popup only fires when
# it's actually relevant instead of on every launch.
NEEDS_RELOGIN=0
NEEDS_RESTART=0

for arg in "$@"; do
  if [[ "$arg" == "--no-install-deps" ]]; then
    SKIP_INSTALL=1
  else
    HOST_ARGS+=("$arg")
  fi
done

if [[ ! -x "$HOST_BIN" ]]; then
  echo "Host binary not found or not executable: $HOST_BIN" >&2
  exit 1
fi

# --- Dependency detection -----------------------------------------------
# cmd_pkgs / elem_pkgs map a required command or GStreamer element to the
# apt package(s) that provide it, so we only install what's actually missing.
missing_pkgs=()
WAYLAND_CAPTURE=1
if [[ "${XDG_SESSION_TYPE:-}" == "x11" ]] || {
  [[ -z "${XDG_SESSION_TYPE:-}" ]] &&
    [[ -n "${DISPLAY:-}" ]] &&
    [[ -z "${WAYLAND_DISPLAY:-}" ]]
}; then
  WAYLAND_CAPTURE=0
fi

check_cmd() {
  local cmd="$1" pkgs="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Missing command: $cmd (needs: $pkgs)"
    for p in $pkgs; do
      missing_pkgs+=("$p")
    done
  fi
}

check_gst_element() {
  local element="$1" pkgs="$2"
  if ! command -v gst-inspect-1.0 >/dev/null 2>&1; then
    return
  fi
  if ! gst-inspect-1.0 "$element" >/dev/null 2>&1; then
    echo "Missing GStreamer element: $element (needs: $pkgs)"
    for p in $pkgs; do
      missing_pkgs+=("$p")
    done
  fi
}

# Commands (checked every run, regardless of what was missing last time).
check_cmd adb "android-sdk-platform-tools android-sdk-platform-tools-common"
check_cmd ffmpeg "ffmpeg"
check_cmd vainfo "vainfo"

if [[ "$WAYLAND_CAPTURE" -eq 1 ]]; then
  check_cmd gst-launch-1.0 "gstreamer1.0-tools"
  check_cmd gst-inspect-1.0 "gstreamer1.0-tools"

  # GStreamer elements used by the PipeWire capture pipeline.
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
  # The X11 extended display is configured with xrandr.
  check_cmd xrandr "x11-xserver-utils"
fi

# De-duplicate the package list while preserving order.
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
fi

# --- Dependency installation ---------------------------------------------
if [[ ${#missing_pkgs[@]} -gt 0 ]]; then
  echo
  echo "Missing packages: ${missing_pkgs[*]}"
  if [[ "$SKIP_INSTALL" -eq 1 ]]; then
    echo "Skipping auto-install (--no-install-deps given). Install manually with:"
    echo "  sudo apt install ${missing_pkgs[*]}"
  elif ! command -v apt-get >/dev/null 2>&1; then
    echo "apt-get not found; install these packages manually with your package manager:"
    echo "  ${missing_pkgs[*]}"
  else
    echo "Attempting to install missing packages (you may be prompted for your sudo password)..."
    if sudo apt-get update && sudo apt-get install -y "${missing_pkgs[@]}"; then
      echo "Dependencies installed successfully."
    else
      echo "Automatic install failed. Install manually with:"
      echo "  sudo apt install ${missing_pkgs[*]}"
    fi
  fi
  echo
fi

# --- VA-API GPU driver check ---------------------------------------------
# vainfo existing doesn't guarantee an encode-capable driver is loaded; probe
# for "EncSlice" support and suggest the right driver package for the GPU.
if command -v vainfo >/dev/null 2>&1; then
  if ! vainfo 2>/dev/null | grep -q "EncSlice"; then
    echo "No VA-API encode-capable driver detected; capture will fall back to slower software x264 encoding."
    driver_pkg=""
    if command -v lspci >/dev/null 2>&1; then
      gpu_info="$(lspci 2>/dev/null | grep -iE 'vga|3d|display')"
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
fi

# --- User group / permission checks (uinput + adb USB access) ------------
# These require re-login to take effect, so we can add the user to the
# groups but cannot make the change effective in this shell session.
user_groups="$(groups "$USER" 2>/dev/null || true)"
needs_groups=()
for g in input plugdev; do
  if [[ ! "$user_groups" =~ (^|[[:space:]])$g($|[[:space:]]) ]]; then
    needs_groups+=("$g")
  fi
done
if [[ ${#needs_groups[@]} -gt 0 ]]; then
  echo "User '$USER' is missing group membership: ${needs_groups[*]} (needed for pen/uinput injection and ADB USB access)."
  if [[ "$SKIP_INSTALL" -eq 0 ]]; then
    if sudo usermod -aG "$(IFS=,; echo "${needs_groups[*]}")" "$USER"; then
      echo "Added $USER to: ${needs_groups[*]}. Log out and back in for this to take effect."
      NEEDS_RELOGIN=1
    else
      echo "Could not add groups automatically. Run manually: sudo usermod -aG $(IFS=,; echo "${needs_groups[*]}") $USER"
    fi
  else
    echo "Run manually: sudo usermod -aG $(IFS=,; echo "${needs_groups[*]}") $USER"
  fi
  echo
fi

# --- /dev/uinput permission check (pen/touch injection) -------------------
# Group membership alone isn't enough: without a udev rule, /dev/uinput is
# root:root mode 0600 and stays that way even after a re-login. Install a
# rule that grants the "input" group access, then apply it immediately so
# injection can work without waiting for a reboot.
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
    # Apply immediately in case udev trigger didn't re-permission the live node.
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

# --- Android USB access (accessory mode) -----------------------------------
# The rule shipped beside this script lets the logged-in user open the tablet
# over USB without adb or Developer options.
AOA_RULE_SOURCE="$SCRIPT_DIR/70-drawsplay-aoa.rules"
AOA_RULE_TARGET="/etc/udev/rules.d/70-drawsplay-aoa.rules"
if [[ -f "$AOA_RULE_SOURCE" ]] && ! cmp -s "$AOA_RULE_SOURCE" "$AOA_RULE_TARGET" 2>/dev/null; then
  if [[ "$SKIP_INSTALL" -eq 0 ]]; then
    echo "Installing USB udev rule: $AOA_RULE_TARGET"
    if sudo install -m 0644 "$AOA_RULE_SOURCE" "$AOA_RULE_TARGET" &&
      sudo udevadm control --reload-rules && sudo udevadm trigger --subsystem-match=usb; then
      echo "USB udev rule installed. Replug the tablet if it was already connected."
    else
      echo "Could not install the USB udev rule. Run manually:"
      echo "  sudo cp $AOA_RULE_SOURCE $AOA_RULE_TARGET && sudo udevadm control --reload-rules && sudo udevadm trigger"
    fi
  else
    echo "Run manually: sudo cp $AOA_RULE_SOURCE $AOA_RULE_TARGET && sudo udevadm control --reload-rules && sudo udevadm trigger"
  fi
  echo
fi

# --- X11 extended display (vkms) -------------------------------------------
# X11 desktops have no virtual-monitor API, so the host extends the desktop
# onto an output of the vkms kernel module (a virtual display that is always
# "connected"). Without such an output the tablet mirrors the desktop.
# Loading vkms reconfigures the current X screen. After the user agrees to
# that change, the launcher also configures the module to load before X at
# future boots, so the virtual output is ready without repeating setup.
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
          # The window manager may switch the new output on by itself; the
          # host switches it on only while the tablet is connected.
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

# --- Log out / restart popup ----------------------------------------------
# Only shown when *this run* actually changed group membership or installed
# a driver that needs a new session/reboot to take effect — not on every
# launch, since those fixes are idempotent and usually only needed once.
if [[ "$NEEDS_RELOGIN" -eq 1 || "$NEEDS_RESTART" -eq 1 ]]; then
  if [[ "$NEEDS_RESTART" -eq 1 ]]; then
    popup_msg="DrawSplay updated system permissions/drivers needed for pen and touch input.\n\nPlease RESTART your computer now for pen/touch input to work correctly."
    title="Restart required"
  else
    popup_msg="DrawSplay added your user to required groups for pen and touch input.\n\nPlease LOG OUT and back in (or restart) for pen/touch input to work correctly."
    title="Log out required"
  fi
  echo "$title: $popup_msg"
  if [[ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]]; then
    if command -v zenity >/dev/null 2>&1; then
      zenity --warning --title="$title" --text="$popup_msg" 2>/dev/null &
    elif command -v kdialog >/dev/null 2>&1; then
      kdialog --title "$title" --sorry "$popup_msg" 2>/dev/null &
    elif command -v xmessage >/dev/null 2>&1; then
      xmessage -center "$popup_msg" 2>/dev/null &
    elif command -v notify-send >/dev/null 2>&1; then
      notify-send -u critical "$title" "$popup_msg" 2>/dev/null || true
    fi
  fi
fi

echo "Starting DrawSplay host..."
exec "$HOST_BIN" "${HOST_ARGS[@]}"
