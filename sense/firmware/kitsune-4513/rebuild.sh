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

# Anything that is not a stock 4513 build is "custom": it cannot match the
# reference SHA1, and it must not overwrite out/kitsune.bin, which is the
# committed reference every other check here compares against. Both consequences
# follow from this one flag rather than being decided separately and drifting.
CUSTOM=""
if [ "$KIT_VER" != "4513" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_DEV_DOMAIN:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_PROD_DOMAIN:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_OTA_FLUSH_FIX:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_DEV_DOMAIN:-}" ] && [ -n "${KITSUNE_PROD_DOMAIN:-}" ]; then
  echo "set one of KITSUNE_DEV_DOMAIN or KITSUNE_PROD_DOMAIN, not both" >&2; exit 1
fi

if [ -n "$CUSTOM" ]; then
  # "-" not ":-", so an empty value stays empty and means "do not compare".
  EXPECT_SHA="${EXPECT_SHA-}"
else
  EXPECT_SHA="${EXPECT_SHA-0c5f639e1290df0e3a5f8641d670923ed71a5e63}"
fi

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

# 2b. Optional: point the DEV endpoint slots at your own domain.
#
# The firmware carries TWO sets of endpoints and picks between them at boot from
# a file on its own flash, which the console command `dev 1` writes and `dev 0`
# clears (kitsune/wifi_cmd.c, Cmd_setDev / load_data_server). Rewriting the DEV
# slots rather than the PROD ones therefore buys a firmware that can be switched
# between your server and the original hello.is names over the serial console,
# with no reflash and no cable, which makes the rollback from a bad domain a
# one-line command instead of a disassembly.
#
# Unset, nothing is touched and the build stays byte-exact. Set, it is not: the
# strings differ, so the SHA1 will not match 4513 and the check below is skipped.
if [ -n "${KITSUNE_DEV_DOMAIN:-}${KITSUNE_PROD_DOMAIN:-}" ]; then
  if [ -n "${KITSUNE_PROD_DOMAIN:-}" ]; then
    SLOT=PROD; DOMAIN_VALUE="$KITSUNE_PROD_DOMAIN"
  else
    SLOT=DEV;  DOMAIN_VALUE="$KITSUNE_DEV_DOMAIN"
  fi
  echo ">> rewriting $SLOT endpoints to *.$DOMAIN_VALUE"
  command -v python3 >/dev/null || { echo "python3 required for KITSUNE_DEV_DOMAIN"; exit 1; }
  DOMAIN="$DOMAIN_VALUE" SLOT="$SLOT" TIME_HOST_OVERRIDE="${KITSUNE_TIME_HOST:-}" python3 - "$WORK/src" <<'REWRITE'
import os, re, sys
root, domain = sys.argv[1], os.environ["DOMAIN"]
slot = os.environ.get("SLOT", "DEV")

ep = os.path.join(root, "kitsune", "endpoints.h")
s = open(ep).read()
# Two of each: the file defines them inside an #ifdef USE_SHA2. 4513 compiles
# the non-SHA2 branch, but rewrite both so this does not silently depend on a
# build flag that could change underneath it.
subs = [(r'(#define\s+%s_DATA_SERVER\s+)"[^"]*"' % slot, r'\1"sense-in.%s"' % domain),
        (r'(#define\s+%s_MESSEJI_SERVER\s+)"[^"]*"' % slot, r'\1"messeji.%s"' % domain)]
for pat, rep in subs:
    s, n = re.subn(pat, rep, s)
    assert n == 2, "endpoints.h: expected 2 matches for %s, got %d" % (pat, n)
open(ep, "w").write(s)

# TIME_HOST is a plain #define with one use site, and it gets a plain string
# swap. Nothing clever.
#
# THIS USED TO INJECT A FUNCTION and it produced an image that does not boot.
# The idea was to give TIME_HOST a DEV twin like the other two endpoints, so
# `dev 1` would return all three to hello.is:
#
#     #include <stdbool.h>
#     extern volatile bool use_dev_server;
#     static char * _time_host(void){
#         return use_dev_server ? "time.hello.is" : "time.<domain>";
#     }
#     #define TIME_HOST _time_host()
#
# It compiled cleanly and hashed correctly onto the device's flash, and the
# bootloader then refused to run it, twice, falling back to the previous image
# every time. Proven on 2026-08-29: build 4515 (with the function) failed twice;
# build 4517, byte-for-byte the same change minus the function, installed first
# time and is running. The `extern volatile bool` declaration was copied from
# get_server() in wifi_cmd.c, so the suspect is the <stdbool.h> include landing
# after sys_time.c's "sl_sync_include_after_simplelink_header.h". Not proven,
# but do not reintroduce this without a device you can reach over UART.
#
# The cost of the simple version is that `dev 1` no longer moves the clock. That
# is acceptable: the other two endpoints still toggle, and time.<domain> is a
# name you control, so the fallback still reaches a working server.
st = os.path.join(root, "kitsune", "sys_time.c")
s = open(st).read()
old = '#define TIME_HOST "time.hello.is"'
assert s.count(old) == 1, "sys_time.c: TIME_HOST not found exactly once"

if slot == "PROD":
    # KITSUNE_TIME_HOST overrides the derived name. Worth having because the
    # clock is the one endpoint that runs over plain HTTP on port 80, with no
    # TLS and therefore no certificate to match, so it can be an IP literal.
    # That matters: "time.orb.example.com" is 14 characters longer than
    # "time.hello.is" and every extra byte in this image lowers the chance the
    # OTA takes, while an IP is the same length as what it replaces.
    host = os.environ.get("TIME_HOST_OVERRIDE") or ("time." + domain)
    s = s.replace(old, '#define TIME_HOST "%s"' % host)
    open(st, "w").write(s)
    print("   PROD endpoints rewritten; TIME_HOST -> %s" % host)

else:
    print("   DEV endpoints rewritten; TIME_HOST left on time.hello.is")
REWRITE
fi

# 2c. Optional: the OTA reliability fixes.
#
# Domain-agnostic C bug fixes in kitsune/fatfs_cmd.c, kept as a real git
# patch (patches/ota-reliability.patch, see patches/README.md) rather than
# string surgery so they compile-check at author time and review as a normal
# diff. Independent of the endpoint rewrite above. Setting this makes the
# image differ from stock 4513, so the SHA1 check is skipped like any custom
# build. --whitespace=nowarn: the 1.9.2 tree has space-before-tab indentation
# the reset-site edits inherit, faithful to the shipped firmware.
if [ -n "${KITSUNE_OTA_FLUSH_FIX:-}" ]; then
  echo ">> applying patches/ota-reliability.patch"
  git -C "$WORK/src" apply --whitespace=nowarn "$HERE/patches/ota-reliability.patch"
  echo "   OTA reliability fixes applied (boot-record write + commit guard)"
fi

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
#
# A customised build goes to a different name. out/kitsune.bin is the committed
# byte-exact 4513 reference and overwriting it would quietly destroy the thing
# every other check in this directory compares against.
mkdir -p "$HERE/out"
if [ -n "$CUSTOM" ]; then
  # Named by build number, because two custom builds are otherwise two writes to
  # one filename and the second silently destroys the first.
  OUT_BIN="$HERE/out/kitsune-custom-$KIT_VER.bin"
  OUT_ELF="$HERE/out/kitsune-custom-$KIT_VER.out"
else
  OUT_BIN="$HERE/out/kitsune.bin"
  OUT_ELF="$HERE/out/kitsune.out"
fi
cp "$WORK/src/kitsune/main/ccs/exe/kitsune.bin" "$OUT_BIN"
cp "$WORK/src/kitsune/main/ccs/exe/kitsune_reord.out" "$OUT_ELF" 2>/dev/null || true
echo ">> wrote $OUT_BIN"
( shasum "$OUT_BIN" 2>/dev/null || sha1sum "$OUT_BIN" )
