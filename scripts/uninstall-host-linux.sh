#!/usr/bin/env bash
set -euo pipefail

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

rm -f "$TARGET_BIN" "$DESKTOP_ENTRY" "$DESKTOP_SHORTCUT" "$ICON_TARGET"

if command -v update-desktop-database >/dev/null 2>&1 && [[ -d "$APPLICATIONS_DIR" ]]; then
  update-desktop-database "$APPLICATIONS_DIR"
fi

echo "DrawSplay launcher, desktop shortcut, icon, and login autostart removed."
echo "DrawSplay settings and logs were kept in ${CONFIG_DIR}/DrawSplay."
echo "Any currently running DrawSplay host will continue until you quit it."
