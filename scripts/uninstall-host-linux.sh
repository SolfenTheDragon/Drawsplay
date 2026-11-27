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
# udev rules (USB accessory, /dev/uinput, spare HDMI connector, EDID override),
# the tablet EDIDs the display helper wrote for KDE, the vkms boot
# configuration written by older installs, a spare HDMI port the host forced
# on, and the host's entries in drm.edid_firmware. Everything that needs root runs in one privileged step, so there
# is at most one password prompt.
SYSTEM_FILES=()
for f in \
  /etc/udev/rules.d/70-drawsplay-aoa.rules \
  /etc/udev/rules.d/70-drawsplay-drm-connector.rules \
  /etc/udev/rules.d/71-drawsplay-edid-override.rules \
  /etc/udev/rules.d/72-drawsplay-vkms-hold.rules \
  /etc/udev/rules.d/99-drawsplay-uinput.rules \
  /etc/modules-load.d/drawsplay-vkms.conf \
  /etc/modprobe.d/drawsplay-vkms.conf \
  /usr/libexec/drawsplay/drawsplay-display-helper \
  /usr/share/polkit-1/actions/com.drawsplay.display-helper.policy \
  /lib/firmware/edid/drawsplay-*.bin; do
  [[ -e "$f" ]] || continue
  # DrawSplay Desktop's dd-host uses the same uinput rule; keep it while dd-host is installed.
  if [[ "$f" == */99-drawsplay-uinput.rules ]] && { command -v dd-host >/dev/null 2>&1 || [[ -x "$HOME/.local/bin/dd-host" ]]; }; then
    echo "Keeping $f: DrawSplay Desktop (dd-host) uses it too."
    continue
  fi
  SYSTEM_FILES+=("$f")
done

# The host's entries in drm.edid_firmware (KDE) go before the ports they point
# at, so a released port reads its real EDID again; entries the host did not
# add stay. With the DrawSplay udev rule installed that needs no root,
# otherwise the privileged step does it, and releases those ports after it.
EDID_PARAM=/sys/module/drm/parameters/edid_firmware
edid_param_has_ours() { grep -q edid/drawsplay- "$EDID_PARAM" 2>/dev/null; }
if edid_param_has_ours; then
  keep=()
  IFS=, read -ra ents <<<"$(tr -d '\000\n' <"$EDID_PARAM")"
  for e in "${ents[@]}"; do [[ -z "$e" || "$e" == *:edid/drawsplay-* ]] || keep+=("$e"); done
  if [[ ${#keep[@]} -gt 0 ]]; then
    (IFS=,; printf '%s' "${keep[*]}") 2>/dev/null >"$EDID_PARAM"
  else
    printf '\0' 2>/dev/null >"$EDID_PARAM"
  fi && echo "Removed DrawSplay's entries from drm.edid_firmware."
fi

# A forced port is connected with no monitor identity (no EDID) behind it, or
# with the tablet EDID the host gives it on KDE (manufacturer DSP, product
# 0x7464). It goes back to autodetect; with the DrawSplay udev rule installed
# that needs no root, otherwise it joins the privileged step.
FORCED_PORTS=()
for dir in /sys/class/drm/card*-HDMI-A-*; do
  [[ -e "$dir/status" ]] || continue
  own_edid=0
  [[ "$(od -An -tx1 -j8 -N4 "$dir/edid" 2>/dev/null | tr -d ' \n')" == 12706474 ]] && own_edid=1
  if [[ "$(cat "$dir/status" 2>/dev/null)" == "connected" ]] && { [[ ! -s "$dir/edid" ]] || [[ "$own_edid" -eq 1 ]]; }; then
    if [[ "$own_edid" -eq 1 ]] && edid_param_has_ours; then
      # Its override is still there: released after it, as root.
      FORCED_PORTS+=("$dir/status")
    elif echo detect >"$dir/status" 2>/dev/null; then
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
  if [[ "$seen" -eq 0 && "$a" =~ ^/lib/firmware/edid/drawsplay-[0-9]{3,4}x[0-9]{3,4}\.bin$ ]]; then
    files+=("$a")
  elif [[ "$seen" -eq 0 ]]; then
    case "$a" in
      /etc/udev/rules.d/70-drawsplay-aoa.rules|/etc/udev/rules.d/70-drawsplay-drm-connector.rules|/etc/udev/rules.d/71-drawsplay-edid-override.rules|/etc/udev/rules.d/72-drawsplay-vkms-hold.rules|/etc/udev/rules.d/99-drawsplay-uinput.rules|/etc/modules-load.d/drawsplay-vkms.conf|/etc/modprobe.d/drawsplay-vkms.conf|/usr/libexec/drawsplay/drawsplay-display-helper|/usr/share/polkit-1/actions/com.drawsplay.display-helper.policy) files+=("$a") ;;
      *) echo "refusing unexpected path: $a" >&2; exit 2 ;;
    esac
  else
    [[ "$a" =~ ^/sys/class/drm/card[0-9]+-HDMI-A-[0-9]+/status$ ]] || { echo "refusing unexpected path: $a" >&2; exit 2; }
    ports+=("$a")
  fi
done
# The host'"'"'s drm.edid_firmware entries go first, so a released port reads
# its real EDID again; entries the host did not add stay.
param=/sys/module/drm/parameters/edid_firmware
if [[ -e "$param" ]] && grep -q edid/drawsplay- "$param" 2>/dev/null; then
  keep=()
  IFS=, read -ra ents <<<"$(tr -d "\000\n" <"$param")"
  for e in "${ents[@]}"; do [[ -z "$e" || "$e" == *:edid/drawsplay-* ]] || keep+=("$e"); done
  if [[ ${#keep[@]} -gt 0 ]]; then (IFS=,; printf "%s" "${keep[*]}") >"$param"; else printf "\0" >"$param"; fi
fi
for p in "${ports[@]}"; do echo detect >"$p"; done
if [[ ${#files[@]} -gt 0 ]]; then
  # A vkms that DrawSplay loaded but never released to X is unused; unload it
  # before the hold rule goes, so X cannot pick up an unchecked device later.
  for c in /sys/class/drm/card[0-9]*; do
    [[ "${c##*/}" =~ ^card[0-9]+$ ]] || continue
    [[ "$(basename "$(readlink -f "$c/device")")" == vkms ]] || continue
    udevadm info -q property -p "$c" 2>/dev/null | grep -qx ID_SEAT=drawsplay-hold && modprobe -r vkms 2>/dev/null
  done
  rm -rf /run/drawsplay
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

# The host's drm.edid_firmware entries (KDE) still there: removed by the
# privileged step.
EDID_PARAM_LEFT=0
edid_param_has_ours && EDID_PARAM_LEFT=1

if [[ ${#SYSTEM_FILES[@]} -gt 0 || ${#FORCED_PORTS[@]} -gt 0 || "$EDID_PARAM_LEFT" -eq 1 ]]; then
  echo "Removing system files DrawSplay installed (asks for your password):"
  [[ ${#SYSTEM_FILES[@]} -gt 0 ]] && printf '  %s\n' "${SYSTEM_FILES[@]}"
  [[ ${#FORCED_PORTS[@]} -gt 0 ]] && printf '  release forced port %s\n' "${FORCED_PORTS[@]}"
  [[ "$EDID_PARAM_LEFT" -eq 1 ]] && echo "  remove DrawSplay's entries from drm.edid_firmware"
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
echo "Group memberships added by the installer (plugdev; older versions also input) are kept; remove them with: sudo gpasswd -d \$USER <group>"

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
