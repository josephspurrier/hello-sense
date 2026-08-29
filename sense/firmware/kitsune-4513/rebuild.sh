#!/usr/bin/env bash
#
# One-command byte-exact rebuild of Sense firmware build 4513 (kitsune 1.9.2)
# from source, on ANY system with Docker.
#
#   ./rebuild.sh
#
# Result: ./out/kitsune.bin, SHA1 0c5f639e1290df0e3a5f8641d670923ed71a5e63
# (146,864 bytes), identical to the device dump and the official 1.9.2 release.
#
# Overridable via environment:
#   KITSUNE_REPO   git URL or path to clone (default: local backup, else GitHub)
#   KITSUNE_TAG    tag/commit to build      (default: 1.9.2)
#   KIT_VER        firmware build number    (default: 4513)
#   EXPECT_SHA     expected .bin SHA1       (default: the 4513 hash)
#   IMAGE          docker image tag         (default: kitsune-byteexact:5.1.5)
#   PLATFORM       docker platform          (default: linux/386)
#
# Requirements: Docker with 32-bit emulation. On Apple Silicon / non-x86 hosts
# run once:  docker run --privileged --rm tonistiigi/binfmt --install 386
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

IMAGE="${IMAGE:-kitsune-byteexact:5.1.5}"
PLATFORM="${PLATFORM:-linux/386}"
KITSUNE_TAG="${KITSUNE_TAG:-1.9.2}"
KIT_VER="${KIT_VER:-4513}"
EXPECT_SHA="${EXPECT_SHA:-0c5f639e1290df0e3a5f8641d670923ed71a5e63}"

# Default source: the local backup clone if present, else the public mirror.
# Walk up from here to find a `github-backup/kitsune` checkout (its location
# relative to this folder depends on where this bundle lives in the tree).
if [ -z "${KITSUNE_REPO:-}" ]; then
  KITSUNE_REPO="https://github.com/hello/kitsune.git"
  d="$HERE"
  while [ "$d" != "/" ]; do
    for cand in "$d/github-backup/kitsune.git" \
                "$d/github-backup/kitsune/.git" \
                "$d/github-backup/kitsune"; do
      if [ -e "$cand" ]; then KITSUNE_REPO="$cand"; break 2; fi
    done
    d=$(dirname "$d")
  done
fi

echo ">> image   : $IMAGE ($PLATFORM)"
echo ">> source  : $KITSUNE_REPO @ $KITSUNE_TAG (KIT_VER=$KIT_VER)"

# 1. Build the toolchain image if it does not exist yet (~5 min first time).
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo ">> building image (first run only)..."
  # Only the compiler is vendored. This used to also demand a ccsv6.tar.gz, left
  # over from when the image installed the full CCS/Eclipse tree; the Dockerfile
  # has not used it since the build switched to driving the generated makefiles
  # with plain make. The stale check aborted every build on a host that did not
  # already have the image cached.
  t="$HERE/toolchain/cgt-5.1.5.tar.gz"
  [ -f "$t" ] || { echo "MISSING $t -- see PROCESS.md 'Toolchain bundle'"; exit 1; }
  docker build --platform "$PLATFORM" -t "$IMAGE" "$HERE"
fi

# 2. Fresh source checkout (clean tree => deterministic generated makefiles).
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
echo ">> cloning source into $WORK/src"
git clone --quiet "$KITSUNE_REPO" "$WORK/src"
git -C "$WORK/src" -c advice.detachedHead=false checkout --quiet "$KITSUNE_TAG"

# Populate the two submodules the build needs (heap_6.c and tinyhttp). Their
# pinned commits at 1.9.2 are vendored as bundles here, because the upstream
# `hello/heap_6` remote no longer exists. This keeps the rebuild fully offline
# and byte-exact (the bundles hold the exact commits d1f85e5 / 9abdeff).
echo ">> populating submodules from vendored bundles"
git -C "$WORK/src" submodule init >/dev/null
git -C "$WORK/src" config submodule."third_party/FreeRTOS/source/portable/MemMang".url "$HERE/submodules/heap_6.bundle"
git -C "$WORK/src" config submodule."kitsune/tinyhttp".url "$HERE/submodules/tinyhttp.bundle"
git -C "$WORK/src" -c protocol.file.allow=always submodule update >/dev/null
# Sanity: the two files whose absence silently truncated earlier builds.
for f in third_party/FreeRTOS/source/portable/MemMang/heap_6.c kitsune/tinyhttp/http.c; do
  [ -f "$WORK/src/$f" ] || { echo "submodule population failed: missing $f"; exit 1; }
done

# 3. Run the full build inside the container.
echo ">> building (headless CCS under $PLATFORM; ~30-90 min under emulation)..."
docker run --rm --platform "$PLATFORM" \
  -e KIT_VER="$KIT_VER" -e EXPECT_SHA="$EXPECT_SHA" \
  -v "$WORK/src:/src" \
  -v "$HERE/build_in_container.sh:/root/build_in_container.sh:ro" \
  -v "$HERE/relink_reforder.sh:/root/relink_reforder.sh:ro" \
  -v "$HERE/extract_bin.py:/root/extract_bin.py:ro" \
  -v "$HERE/generated_makefiles.tar.gz:/root/generated_makefiles.tar.gz:ro" \
  "$IMAGE" bash /root/build_in_container.sh

# 4. Copy result out.
mkdir -p "$HERE/out"
cp "$WORK/src/kitsune/main/ccs/exe/kitsune.bin" "$HERE/out/kitsune.bin"
cp "$WORK/src/kitsune/main/ccs/exe/kitsune_reord.out" "$HERE/out/kitsune.out" 2>/dev/null || true
echo ">> wrote $HERE/out/kitsune.bin"
( cd "$HERE/out" && shasum kitsune.bin 2>/dev/null || sha1sum kitsune.bin )
