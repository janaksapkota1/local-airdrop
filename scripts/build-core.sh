#!/usr/bin/env bash
# Builds the Go core for every target:
#   core/bin/homedropd                 Linux daemon/CLI (bundled into the Linux app)
#   app/android/app/libs/homedrop.aar  Android library (gomobile)
#
# usage: scripts/build-core.sh [linux|android|all]   (default: all)
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
what=${1:-all}

export ANDROID_HOME=${ANDROID_HOME:-$HOME/Android/Sdk}
export PATH=$HOME/go/bin:$PATH

if [[ $what == linux || $what == all ]]; then
  echo "→ building core/bin/homedropd"
  (cd core && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/homedropd ./cmd/homedropd)
fi

if [[ $what == android || $what == all ]]; then
  if [[ -z ${ANDROID_NDK_HOME:-} ]]; then
    ANDROID_NDK_HOME=$(ls -d "$ANDROID_HOME"/ndk/* 2>/dev/null | sort -V | tail -1)
    export ANDROID_NDK_HOME
  fi
  # gomobile needs a full JDK (javac); prefer 21 if it is installed.
  if [[ -d /usr/lib/jvm/java-21-openjdk-amd64 ]]; then
    export JAVA_HOME=/usr/lib/jvm/java-21-openjdk-amd64 PATH=/usr/lib/jvm/java-21-openjdk-amd64/bin:$PATH
  fi
  echo "→ building app/android/app/libs/homedrop.aar (NDK: $ANDROID_NDK_HOME)"
  mkdir -p app/android/app/libs
  (cd core && gomobile bind -target=android/arm64,android/arm,android/amd64 -androidapi 26 \
     -javapkg com.janak.homedrop.core -trimpath -ldflags="-s -w" \
     -o "$ROOT/app/android/app/libs/homedrop.aar" ./mobile)
fi
echo "done"
