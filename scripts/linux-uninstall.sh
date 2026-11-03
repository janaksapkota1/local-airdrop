#!/usr/bin/env bash
# Removes HomeDrop for the current user. Received files are left alone.
# Pass --purge to also delete this device's keys and trusted-device list.
set -uo pipefail
DEST="$HOME/.local/share/homedrop"

systemctl --user disable --now homedropd >/dev/null 2>&1
rm -f "$HOME/.config/systemd/user/homedropd.service"
pkill -f "$DEST/homedropd daemon" 2>/dev/null
rm -rf "$DEST" "$HOME/.local/bin/homedrop" "$HOME/.local/bin/homedropd" \
       "$HOME/.local/share/applications/homedrop.desktop"
if [[ ${1:-} == --purge ]]; then
  rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/homedrop"
  echo "✓ Removed HomeDrop and its keys"
else
  echo "✓ Removed HomeDrop (keys kept in ~/.config/homedrop; use --purge to delete)"
fi
