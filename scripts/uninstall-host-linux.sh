#!/usr/bin/env bash
set -euo pipefail

PURGE=0
for arg in "$@"; do
  case "$arg" in
    --purge) PURGE=1 ;;
    -h|--help)
      echo "Usage: uninstall-drawsplay-host-linux.sh [--purge]"
      echo "  --purge  also delete DrawSplay settings, pairing data and logs"
      exit 0
      ;;
    *) echo "Unknown option: $arg" >&2; exit 2 ;;
  esac
done

if [[ -z "${HOME:-}" ]]; then
  echo "HOME is not set; cannot locate the DrawSplay installation." >&2
  exit 1
fi

TARGET_BIN="${HOME}/.local/bin/drawsplay-host"
ICON_TARGET="${HOME}/.local/share/icons/hicolor/64x64/apps/drawsplay-host.png"
APPLICATIONS_DIR="${HOME}/.local/share/applications"
DESKTOP_ENTRY="${APPLICATIONS_DIR}/drawsplay-host.desktop"
CONFIG_DIR="${XDG_CONFIG_HOME:-${HOME}/.config}"
AUTOSTART_ENTRY="${CONFIG_DIR}/autostart/drawsplay-host.desktop"
DESKTOP_DIR=""

if command -v xdg-user-dir >/dev/null 2>&1; then
  DESKTOP_DIR="$(xdg-user-dir DESKTOP || true)"
fi
if [[ -z "$DESKTOP_DIR" ]]; then
  DESKTOP_DIR="${HOME}/Desktop"
fi
DESKTOP_SHORTCUT="${DESKTOP_DIR}/drawsplay-host.desktop"

if [[ -x "$TARGET_BIN" ]]; then
  "$TARGET_BIN" -service uninstall
else
  rm -f "$AUTOSTART_ENTRY"
  rm -f \
    "${CONFIG_DIR}/systemd/user/graphical-session.target.wants/drawsplay-host.service"
  if [[ -n "${XDG_RUNTIME_DIR:-}" ]]; then
    rm -f "${XDG_RUNTIME_DIR}/systemd/user/graphical-session.target.wants/drawsplay-host.service"
  fi
fi

# Stop the running host first (SIGTERM is a clean shutdown: it switches off the
# virtual displays it holds), then force it if it has not exited.
stop_running_host() {
  command -v pgrep >/dev/null 2>&1 || return 0
  local pids
  pids="$(pgrep -u "$(id -u)" -x drawsplay-host 2>/dev/null || true)"
  [[ -n "$pids" ]] || return 0
  echo "Stopping the running DrawSplay host..."
  # shellcheck disable=SC2086
  kill -TERM $pids 2>/dev/null || true
  local i
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    pgrep -u "$(id -u)" -x drawsplay-host >/dev/null 2>&1 || { echo "DrawSplay host stopped."; return 0; }
    sleep 0.5
  done
  pids="$(pgrep -u "$(id -u)" -x drawsplay-host 2>/dev/null || true)"
  # shellcheck disable=SC2086
  [[ -z "$pids" ]] || kill -KILL $pids 2>/dev/null || true
  echo "DrawSplay host did not exit in time and was killed."
}
stop_running_host

rm -f "$TARGET_BIN" "$DESKTOP_ENTRY" "$DESKTOP_SHORTCUT" "$ICON_TARGET"

if command -v update-desktop-database >/dev/null 2>&1 && [[ -d "$APPLICATIONS_DIR" ]]; then
  update-desktop-database "$APPLICATIONS_DIR"
fi

# --- System files the installer or the host may have created ----------------
# udev rules (USB accessory, /dev/uinput, spare HDMI connector), the vkms boot
# configuration written by older installs, and a spare HDMI port the host
# forced on. Everything that needs root runs in one privileged step, so there
# is at most one password prompt.
SYSTEM_FILES=()
for f in \
  /etc/udev/rules.d/70-drawsplay-aoa.rules \
  /etc/udev/rules.d/70-drawsplay-drm-connector.rules \
  /etc/udev/rules.d/99-drawsplay-uinput.rules \
  /etc/modules-load.d/drawsplay-vkms.conf \
  /etc/modprobe.d/drawsplay-vkms.conf \
  /usr/libexec/drawsplay/drawsplay-display-helper \
  /usr/share/polkit-1/actions/com.drawsplay.display-helper.policy; do
  [[ -e "$f" ]] && SYSTEM_FILES+=("$f")
done

# A forced port is connected with no monitor identity (no EDID) behind it. It
# goes back to autodetect; with the DrawSplay udev rule installed that needs
# no root, otherwise it joins the privileged step.
FORCED_PORTS=()
for dir in /sys/class/drm/card*-HDMI-A-*; do
  [[ -e "$dir/status" ]] || continue
  if [[ "$(cat "$dir/status" 2>/dev/null)" == "connected" && ! -s "$dir/edid" ]]; then
    if echo detect >"$dir/status" 2>/dev/null; then
      echo "Released the forced display port: $dir/status"
    else
      FORCED_PORTS+=("$dir/status")
    fi
  fi
done

# Runs the privileged cleanup. Only the paths this script knows are accepted.
ROOT_CLEANUP='
set +e
files=(); ports=(); seen=0
for a in "$@"; do
  if [[ "$a" == "--" ]]; then seen=1; continue; fi
  if [[ "$seen" -eq 0 ]]; then
    case "$a" in
      /etc/udev/rules.d/70-drawsplay-aoa.rules|/etc/udev/rules.d/70-drawsplay-drm-connector.rules|/etc/udev/rules.d/99-drawsplay-uinput.rules|/etc/modules-load.d/drawsplay-vkms.conf|/etc/modprobe.d/drawsplay-vkms.conf|/usr/libexec/drawsplay/drawsplay-display-helper|/usr/share/polkit-1/actions/com.drawsplay.display-helper.policy) files+=("$a") ;;
      *) echo "refusing unexpected path: $a" >&2; exit 2 ;;
    esac
  else
    [[ "$a" =~ ^/sys/class/drm/card[0-9]+-HDMI-A-[0-9]+/status$ ]] || { echo "refusing unexpected path: $a" >&2; exit 2; }
    ports+=("$a")
  fi
done
for p in "${ports[@]}"; do echo detect >"$p"; done
if [[ ${#files[@]} -gt 0 ]]; then
  rm -f "${files[@]}"
  rmdir /usr/libexec/drawsplay 2>/dev/null
  udevadm control --reload-rules
  udevadm trigger --subsystem-match=drm --subsystem-match=usb --action=change
  for f in "${files[@]}"; do
    # The vkms module was loaded by that config; unload it if nothing uses it.
    [[ "$f" == *drawsplay-vkms.conf ]] && { modprobe -r vkms 2>/dev/null; break; }
  done
fi
exit 0
'

# Runs a command as root: directly, with passwordless sudo, with sudo on a
# terminal (asks for the password), or through a graphical polkit prompt.
run_as_root() {
  if [[ "$EUID" -eq 0 ]]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    sudo "$@"
  elif command -v sudo >/dev/null 2>&1 && [[ -t 0 ]]; then
    sudo "$@"
  elif command -v pkexec >/dev/null 2>&1; then
    pkexec "$@"
  else
    return 127
  fi
}

if [[ ${#SYSTEM_FILES[@]} -gt 0 || ${#FORCED_PORTS[@]} -gt 0 ]]; then
  echo "Removing system files DrawSplay installed (asks for your password):"
  [[ ${#SYSTEM_FILES[@]} -gt 0 ]] && printf '  %s\n' "${SYSTEM_FILES[@]}"
  [[ ${#FORCED_PORTS[@]} -gt 0 ]] && printf '  release forced port %s\n' "${FORCED_PORTS[@]}"
  if run_as_root bash -c "$ROOT_CLEANUP" bash "${SYSTEM_FILES[@]}" -- "${FORCED_PORTS[@]}"; then
    echo "System files removed."
  else
    echo "Could not get administrator rights. Run: sudo rm -f ${SYSTEM_FILES[*]}" >&2
  fi
fi

echo "DrawSplay launcher, desktop shortcut, icon, and login autostart removed."
if [[ "$PURGE" -eq 1 ]]; then
  rm -rf "${CONFIG_DIR}/DrawSplay"
  echo "DrawSplay settings, pairing data and logs removed."
else
  echo "DrawSplay settings and logs were kept in ${CONFIG_DIR}/DrawSplay (run with --purge to delete them)."
fi
echo "Group memberships added by the installer (input, plugdev) are kept; remove them with: sudo gpasswd -d \$USER <group>"

# --- Restart prompt ----------------------------------------------------------
# Kernel modules and display state DrawSplay touched (for example vkms while
# the X server uses it) are only fully released by a restart.
prompt_reboot() {
  local title="DrawSplay uninstall"
  local msg="DrawSplay was uninstalled. Restart your computer to complete the uninstall."
  local now=0
  if [[ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]] && command -v zenity >/dev/null 2>&1; then
    zenity --question --title="$title" --text="$msg" --ok-label="Reboot" --cancel-label="Do it later" --no-wrap 2>/dev/null && now=1
  elif [[ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]] && command -v kdialog >/dev/null 2>&1; then
    kdialog --title "$title" --yes-label "Reboot" --no-label "Do it later" --yesno "$msg" 2>/dev/null && now=1
  elif [[ -t 0 && -t 1 ]]; then
    local reply=""
    read -r -p "$msg Reboot now? [y/N] " reply || true
    [[ "$reply" =~ ^[Yy]([Ee][Ss])?$ ]] && now=1
  else
    echo "$msg"
    return 0
  fi
  if [[ "$now" -eq 1 ]]; then
    echo "Restarting..."
    systemctl reboot 2>/dev/null || run_as_root systemctl reboot || echo "Could not restart; please restart your computer." >&2
  else
    echo "Restart your computer later to complete the uninstall."
  fi
}
if [[ "${DRAWSPLAY_NO_REBOOT_PROMPT:-0}" != "1" ]]; then
  prompt_reboot
fi
