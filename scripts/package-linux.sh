#!/usr/bin/env bash
# Builds dist/homedrop-linux-x64.tar.gz: copy it to another Linux PC, then
#   tar xzf homedrop-linux-x64.tar.gz && cd homedrop-linux-x64 && ./install.sh
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH=$HOME/dev/flutter/bin:$PATH

scripts/build-core.sh linux
(cd app && flutter build linux --release)

OUT=dist/homedrop-linux-x64
rm -rf "$OUT" "$OUT.tar.gz"
mkdir -p "$OUT"
cp -r app/build/linux/x64/release/bundle "$OUT/homedrop"
cp scripts/linux-install.sh "$OUT/install.sh"
cp scripts/linux-uninstall.sh "$OUT/uninstall.sh"
chmod +x "$OUT"/*.sh
tar -C dist -czf "$OUT.tar.gz" homedrop-linux-x64
rm -rf "$OUT"
echo "→ $(du -h "$OUT.tar.gz" | cut -f1)  $OUT.tar.gz"
