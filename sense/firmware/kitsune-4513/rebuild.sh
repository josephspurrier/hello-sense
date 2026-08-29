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
  DOMAIN="$DOMAIN_VALUE" SLOT="$SLOT" python3 - "$WORK/src" <<'REWRITE'
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

# TIME_HOST has no DEV twin upstream: it is a plain #define with exactly one use
# site. Give it one, so `dev 1` switches all three endpoints rather than two and
# leaves the clock talking to a domain that no longer answers.
st = os.path.join(root, "kitsune", "sys_time.c")
s = open(st).read()
old = '#define TIME_HOST "time.hello.is"'
assert s.count(old) == 1, "sys_time.c: TIME_HOST not found exactly once"
# In DEV mode the new domain is what `dev 1` selects. In PROD mode it is the
# default and `dev 1` is the way back to hello.is, which the LAN DNS answers.
if slot == "PROD":
    on_dev, on_prod = "time.hello.is", "time." + domain
else:
    on_dev, on_prod = "time." + domain, "time.hello.is"
new = ('#include <stdbool.h>\n'   # wifi_cmd.h brings in stdint, not stdbool
       'extern volatile bool use_dev_server;\n'
       'static char * _time_host(void){\n'
       '\treturn use_dev_server ? "%s" : "%s";\n'
       '}\n'
       '#define TIME_HOST _time_host()' % (on_dev, on_prod))
open(st, "w").write(s.replace(old, new))
print("   %s endpoints rewritten in endpoints.h and sys_time.c" % slot)
REWRITE
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
