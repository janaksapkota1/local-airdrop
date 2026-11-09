#!/usr/bin/env bash
# HomeDrop installer for Linux. No root needed; installs for the current user.
#
#   ./install.sh                 install app + menu entry
#   ./install.sh --autostart     also start the receiver at login (systemd --user)
#   ./install.sh --skip-checks   don't check system libraries (install anyway)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="$HERE/homedrop"
DEST="$HOME/.local/share/homedrop"
BIN="$HOME/.local/bin"
APPS="$HOME/.local/share/applications"
# Tools like ldconfig/getconf live in sbin, which is often not on a user's PATH.
PATH="$PATH:/usr/local/sbin:/usr/sbin:/sbin"

autostart=0 checks=1
for arg in "$@"; do
  case $arg in
    --autostart)   autostart=1 ;;
    --skip-checks) checks=0 ;;
    -h|--help)     sed -n '2,6p' "$0"; exit 0 ;;
    *) echo "Unknown option: $arg" >&2; exit 2 ;;
  esac
done

if [[ ! -x $SRC/homedrop || ! -f $SRC/homedropd ]]; then
  echo "Can't find the app next to this script ($SRC). Run install.sh from the extracted folder." >&2
  exit 1
fi

# --- checks -------------------------------------------------------------
if [[ $(uname -m) != x86_64 ]]; then
  echo "This build is for x86_64; this machine is $(uname -m)." >&2; exit 1
fi

# Distro family, for install hints.
distro=""
if [[ -r /etc/os-release ]]; then
  distro=$( . /etc/os-release; echo "${ID:-} ${ID_LIKE:-}" )
fi
gtk_install_cmd() {
  case " $distro " in
    *" debian "*|*" ubuntu "*) echo "sudo apt install libgtk-3-0" ;;
    *" fedora "*|*" rhel "*)   echo "sudo dnf install gtk3" ;;
    *" arch "*)                echo "sudo pacman -S gtk3" ;;
    *" suse "*|*" opensuse "*) echo "sudo zypper install libgtk-3-0" ;;
    *) echo "" ;;
  esac
}

if (( checks )); then
  # glibc version (no pipes, so pipefail can't produce false failures).
  glibc=$(getconf GNU_LIBC_VERSION 2>/dev/null || true)   # "glibc 2.39"
  glibc=${glibc##* }
  if [[ -z $glibc ]] && command -v ldd >/dev/null; then
    first=$(ldd --version 2>&1 || true); first=${first%%$'\n'*}
    [[ $first =~ ([0-9]+\.[0-9]+)[[:space:]]*$ ]] && glibc=${BASH_REMATCH[1]}
  fi
  if [[ -z $glibc ]]; then
    echo "! Couldn't detect glibc version (musl-based distro?). HomeDrop needs glibc 2.34+; continuing anyway." >&2
  elif [[ $(printf '%s\n%s\n' 2.34 "$glibc" | sort -V | head -n1) != 2.34 ]]; then
    echo "Needs glibc 2.34+ (Ubuntu 22.04 / Debian 12 / Fedora 35 or newer); found $glibc." >&2
    echo "(Run with --skip-checks to install anyway.)" >&2
    exit 1
  fi

  # Ask the dynamic loader what the app actually can't find, instead of
  # guessing via ldconfig (which misses libs and isn't always on PATH).
  if command -v ldd >/dev/null; then
    missing=$(
      for f in "$SRC/homedrop" "$SRC"/lib/*.so; do
        ldd "$f" 2>&1 || true
      done | grep -E 'not found' | sed -E 's/^[[:space:]]*//; s/ => not found//' | sort -u || true
    )
    if [[ -n $missing ]]; then
      echo "HomeDrop needs some system libraries that aren't installed:" >&2
      sed 's/^/    /' <<<"$missing" >&2
      cmd=$(gtk_install_cmd)
      if [[ -n $cmd ]]; then
        echo "Install them with:  $cmd" >&2
      else
        echo "Install GTK 3 with your package manager (package usually named gtk3 or libgtk-3-0)." >&2
      fi
      echo "(Or run with --skip-checks to install anyway.)" >&2
      exit 1
    fi
  else
    echo "! ldd not found; skipping library check." >&2
  fi
fi

# --- install ------------------------------------------------------------
# Stop a running receiver so its binary can be replaced.
systemctl --user stop homedropd >/dev/null 2>&1 || true
pkill -f "$DEST/homedropd daemon" 2>/dev/null || true

rm -rf "$DEST"
mkdir -p "$DEST" "$BIN" "$APPS"
cp -r "$SRC/." "$DEST/"
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
case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo "  Note: $BIN is not on your PATH; open HomeDrop from the app menu or run $DEST/homedrop" ;;
esac

# --- optional: receive at login -----------------------------------------
if (( autostart )); then
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
  if systemctl --user daemon-reload 2>/dev/null &&
     systemctl --user enable --now homedropd >/dev/null 2>&1; then
    echo "✓ Receiver starts automatically at login"
  else
    echo "! Couldn't enable autostart (no systemd user session?). The receiver still runs while the app is open." >&2
  fi
elif systemctl --user is-enabled homedropd >/dev/null 2>&1; then
  systemctl --user start homedropd >/dev/null 2>&1 || true   # restart after upgrade
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
