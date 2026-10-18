#!/usr/bin/env bash
# Builds the optional native capture/encode helper (Linux X11 + VA-API) and
# copies it next to the host binary when a destination is given.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
make -C "$root/native/drawsplay-media" "$@"
if [[ -n "${DRAWSPLAY_HELPER_DEST:-}" ]]; then
  install -m 0755 "$root/native/drawsplay-media/build/drawsplay-media" "$DRAWSPLAY_HELPER_DEST/"
fi
