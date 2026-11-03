#!/usr/bin/env bash
# HomeDrop installer for Linux. No root needed; installs for the current user.
#
#   ./install.sh               install app + menu entry
#   ./install.sh --autostart   also start the receiver at login (systemd --user)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
DEST="$HOME/.local/share/homedrop"
BIN="$HOME/.local/bin"
APPS="$HOME/.local/share/applications"

# --- checks -------------------------------------------------------------
if [[ $(uname -m) != x86_64 ]]; then
  echo "This build is for x86_64; this machine is $(uname -m)." >&2; exit 1
fi
glibc=$(ldd --version 2>/dev/null | head -1 | grep -oE '[0-9]+\.[0-9]+$' || echo 0)
if [[ $(printf '%s\n2.34\n' "$glibc" | sort -V | head -1) != 2.34 ]]; then
  echo "Needs glibc 2.34+ (Ubuntu 22.04 / Debian 12 / Fedora 35 or newer); found $glibc." >&2; exit 1
fi
if ! ldconfig -p 2>/dev/null | grep -q libgtk-3.so.0; then
  echo "GTK 3 is missing. Install it first:"
  echo "  Debian/Ubuntu: sudo apt install libgtk-3-0"
  echo "  Fedora:        sudo dnf install gtk3"
  echo "  Arch:          sudo pacman -S gtk3"
  exit 1
fi

# --- install ------------------------------------------------------------
# Stop a running receiver so its binary can be replaced.
pkill -f "$DEST/homedropd daemon" 2>/dev/null || true

rm -rf "$DEST"
mkdir -p "$DEST" "$BIN" "$APPS"
cp -r "$HERE/homedrop/." "$DEST/"
chmod +x "$DEST/homedrop" "$DEST/homedropd"
ln -sf "$DEST/homedrop" "$BIN/homedrop"
ln -sf "$DEST/homedropd" "$BIN/homedropd"

cat > "$APPS/homedrop.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=HomeDrop
Comment=Send files between your devices
Exec=$DEST/homedrop
Icon=network-transmit-receive
Terminal=false
Categories=Network;FileTransfer;Utility;
EOF
update-desktop-database "$APPS" >/dev/null 2>&1 || true
echo "✓ Installed to $DEST (menu entry: HomeDrop, command: homedrop)"

# --- optional: receive at login -----------------------------------------
if [[ ${1:-} == --autostart ]]; then
  mkdir -p "$HOME/.config/systemd/user"
  cat > "$HOME/.config/systemd/user/homedropd.service" <<EOF
[Unit]
Description=HomeDrop file receiver
After=network-online.target

[Service]
ExecStart=$DEST/homedropd daemon
Restart=on-failure

[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable --now homedropd >/dev/null
  echo "✓ Receiver starts automatically at login"
fi

# --- firewall hint ------------------------------------------------------
if command -v ufw >/dev/null && sudo -n ufw status 2>/dev/null | grep -q "Status: active"; then
  echo "! ufw is active. Allow HomeDrop with:  sudo ufw allow 53530"
elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
  echo "! firewalld is active. Allow HomeDrop with:"
  echo "    sudo firewall-cmd --permanent --add-port=53530/tcp --add-port=53530/udp && sudo firewall-cmd --reload"
else
  echo "  (If you use a firewall, allow port 53530 TCP+UDP.)"
fi
