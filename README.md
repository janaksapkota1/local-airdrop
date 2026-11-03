# HomeDrop

AirDrop-style file transfer between your own devices (Linux ↔ Android) on the
same Wi-Fi. Encrypted (TLS 1.3, pinned device keys), resumable, and fast.

```
core/     Go engine: discovery, transfer protocol, control API, CLI (homedropd)
app/      Flutter app for Linux and Android
scripts/  build-core.sh, homedropd.service (systemd autostart)
```

## Build
```bash
export PATH=$HOME/dev/flutter/bin:$PATH JAVA_HOME=/usr/lib/jvm/java-21-openjdk-amd64
scripts/build-core.sh                         # Go: Linux binary + Android .aar
cd app
flutter build linux --release                 # → build/linux/x64/release/bundle/homedrop
flutter build apk --release --split-per-abi   # → build/app/outputs/flutter-apk/
```

## Install
**Linux:** run `app/build/linux/x64/release/bundle/homedrop`. The background
receiver keeps running after you close the window. To start it at login, see
`scripts/homedropd.service`. If you use ufw: `sudo ufw allow 53530`.

**Android:** copy `app-arm64-v8a-release.apk` (almost every phone made after
2017) to the phone and open it, or with USB debugging on:
`~/Android/Sdk/platform-tools/adb install app/build/app/outputs/flutter-apk/app-arm64-v8a-release.apk`.
Allow notifications when asked, so incoming files can be accepted while the app
is closed.

## Use
Open HomeDrop on both devices, tap the other device, and pick files, a folder
(Linux), the clipboard, or text. The first time, both screens show a 4-digit code.
Check that the codes match and accept. On Android you can also use **Share → HomeDrop**
from any app. Received files go to `~/Downloads/HomeDrop` (Linux) or
`Download/HomeDrop` (Android).

If devices don't see each other (guest Wi-Fi, firewall), use the network icon to
connect by IP address.
