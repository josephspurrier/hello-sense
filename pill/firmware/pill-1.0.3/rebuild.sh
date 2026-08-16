#!/usr/bin/env bash
#
# Rebuild the Sleep Pill v1 firmware (pill+pill_PVT1) at tag 1.0.3 from source,
# on any system with Docker. This is the build with a WORKING ANT radio (see
# PROCESS.md for why 1.2.1 does not pair and 1.0.3 does).
#
#   ./rebuild.sh
#
# Result: ./out/pill+pill_PVT1.bin (version "1.0.3", ~42.5 KB) plus the matching
# bootloader and the app-settings CRC blob for flashing.
#
# Overridable via environment:
#   KODOBANNIN_REPO  git URL/path to clone   (default: local backup, else GitHub)
#   KODOBANNIN_TAG   tag to build            (default: 1.0.3)
#   NRF51_SDK        path to the nRF51 SDK   (default: search; see below)
#   IMAGE            docker image tag        (default: pill-nrf-build)
#   PLATFORM         docker platform         (default: linux/amd64)
#
# The nRF51 SDK (v5.2.0 with S310 v1.0.0 headers) is NOT vendored here — it is
# Nordic-licensed. Point NRF51_SDK at a local copy, or fetch the public mirror
# https://github.com/tdwebste/nRF51SDK . Its layout must contain
# `nrf51422/Include/nrf51.h`. The S310 SoftDevice *binary* is only needed to
# FLASH (not to build); see flash/README notes.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

IMAGE="${IMAGE:-pill-nrf-build}"
PLATFORM="${PLATFORM:-linux/amd64}"
KODOBANNIN_TAG="${KODOBANNIN_TAG:-1.0.3}"

# --- locate source repo (local backup preferred, else public) ---
if [ -z "${KODOBANNIN_REPO:-}" ]; then
  KODOBANNIN_REPO="https://github.com/hello/kodobannin.git"
  d="$HERE"
  while [ "$d" != "/" ]; do
    for c in "$d/github-backup/kodobannin/.git" "$d/github-backup/kodobannin.git" "$d/github-backup/kodobannin"; do
      [ -e "$c" ] && { KODOBANNIN_REPO="$c"; break 2; }
    done
    d=$(dirname "$d")
  done
fi

# --- locate the nRF51 SDK (must contain nrf51422/Include/nrf51.h) ---
if [ -z "${NRF51_SDK:-}" ]; then
  d="$HERE"
  while [ "$d" != "/" ]; do
    for c in "$d/working-files/nrf51sdk_src/Nordic" "$d/nrf51sdk/Nordic" "$d/nRF51SDK/Nordic"; do
      [ -f "$c/nrf51422/Include/nrf51.h" ] && { NRF51_SDK="$c"; break 2; }
    done
    d=$(dirname "$d")
  done
fi
if [ -z "${NRF51_SDK:-}" ] || [ ! -f "$NRF51_SDK/nrf51422/Include/nrf51.h" ]; then
  echo "ERROR: nRF51 SDK not found. Set NRF51_SDK=<path to SDK root> (the dir that"
  echo "contains nrf51422/Include/nrf51.h). Get it from https://github.com/tdwebste/nRF51SDK"
  exit 1
fi

echo ">> image  : $IMAGE ($PLATFORM)"
echo ">> source : $KODOBANNIN_REPO @ $KODOBANNIN_TAG"
echo ">> SDK    : $NRF51_SDK"

# 1. Build the GCC-4.7 image (first run only; downloads the toolchain).
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo ">> building toolchain image (first run only)..."
  docker build --platform "$PLATFORM" -t "$IMAGE" "$HERE"
fi

# 2. Fresh checkout + the one public submodule the build needs (micro-ecc).
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
echo ">> cloning source"
git clone --quiet "$KODOBANNIN_REPO" "$WORK/kodobannin"
git -C "$WORK/kodobannin" -c advice.detachedHead=false checkout --quiet "$KODOBANNIN_TAG"
git -C "$WORK/kodobannin" submodule update --init --recursive -- micro-ecc >/dev/null 2>&1 \
  || git -C "$WORK/kodobannin" -c protocol.file.allow=always submodule update --init -- micro-ecc >/dev/null 2>&1 || true

# 3. Build in the container. The SDK is mounted at a NON-nested path (/sdk) and
#    symlinked in — a nested mount under the source dir is unreliable on
#    Docker Desktop / Rosetta.
echo ">> building pill+pill_PVT1 (tag $KODOBANNIN_TAG)..."
docker run --rm --platform "$PLATFORM" \
  -v "$WORK/kodobannin:/src/kodobannin" \
  -v "$NRF51_SDK:/sdk:ro" \
  -w /src/kodobannin \
  "$IMAGE" \
  sh -c 'rm -rf nRF51_SDK nRF51_SDK_real; ln -s /sdk nRF51_SDK
    test -f nRF51_SDK/nrf51422/Include/nrf51.h || { echo "SDK mount failed"; exit 1; }
    make KODOBANNIN_GCC_ROOT=/opt/gcc-arm-none-eabi-4_7-2013q3 pill+pill_PVT1 2>&1 | tail -3'

# 4. Collect artifacts.
mkdir -p "$HERE/out"
cp "$WORK/kodobannin/build/pill+pill_PVT1.bin" \
   "$WORK/kodobannin/build/pill+pill_PVT1.hex" \
   "$WORK/kodobannin/build/pill+pill_PVT1.elf" \
   "$WORK/kodobannin/build/bootloader+pill_PVT1.bin" \
   "$WORK/kodobannin/build/bootloader+pill_PVT1.hex" "$HERE/out/"

# 5. App-settings CRC blob (portable; the Makefile's own step is broken on Linux).
python3 "$HERE/gen_app_settings.py" "$HERE/out/pill+pill_PVT1.bin" "$HERE/out/app_settings.crc.bin"

# 6. Verify.
echo ">> verify"
VER=$(strings "$HERE/out/pill+pill_PVT1.bin" | grep -E '^1\.[0-9]+\.[0-9]+' | head -1 || true)
KEY=$(xxd -p "$HERE/out/pill+pill_PVT1.bin" | tr -d '\n' | grep -o 'a8ac207a1d72e34d' | head -1 || true)
echo "   version string : ${VER:-<none>}"
echo "   ANT network key: $([ -n "$KEY" ] && echo present || echo MISSING)"
( cd "$HERE/out" && { shasum pill+pill_PVT1.bin 2>/dev/null || sha1sum pill+pill_PVT1.bin; } )
echo ">> done -> $HERE/out/"
