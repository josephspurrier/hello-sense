#!/usr/bin/env bash
#
# One-command byte-exact rebuild of the SENSE WITH VOICE firmware (CC3220SF),
# kitsune build 6149 (tag 1p5_ext_rc7, commit fab199b3), from source.
#
#   ./rebuild.sh
#
# Byte-exact target: ./out/kitsune.bin, SHA1
#   90453011b136916d3b67aa302e86b7526c074420  (360,984 bytes),
# identical to nobita's archived pvt build (reference/pvt-kitsune.bin). This is
# the CC3220SF application image; on the device it lives at /ota/mcuimg1.bin,
# loaded PLAIN (unsigned) by the signed boot manager. NOTE: the device's running
# image is a slightly later build than this pvt snapshot, so we validate the
# recipe against the pvt snapshot (known commit) and then build custom on top.
#
# Differs from the CC3200 orb recipe (../kitsune-4513) only in: the checked-out
# commit, the CC3220sf.cmd linker file + __SF_DEBUG__ define, the endpoint set
# (voice adds a speech server), and the byte-exact target. Everything else
# (CGT 5.1.5, linux/386 container, submodule bundles, reorder-relink, extract_bin)
# is unchanged. See BUILD_NOTES.md for the makefile-generation step that remains.
#
# Overridable via environment:
#   KITSUNE_REPO   git URL or path to clone (default: local backup, else GitHub)
#   KITSUNE_TAG    tag/commit to build      (default: 1p5_ext_rc7)
#   KIT_VER        firmware build number    (default: 6149)
#   EXPECT_SHA     expected .bin SHA1       (default: the 6149 hash)
#   IMAGE          docker image tag         (default: kitsune-byteexact:5.1.5)
#   PLATFORM       docker platform          (default: linux/386)
#
# Requirements: Docker with 32-bit emulation. On Apple Silicon / non-x86 hosts
# run once:  docker run --privileged --rm tonistiigi/binfmt --install 386
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

IMAGE="${IMAGE:-kitsune-byteexact:5.1.5}"
PLATFORM="${PLATFORM:-linux/386}"
KITSUNE_TAG="${KITSUNE_TAG:-1p5_ext_rc7}"
KIT_VER="${KIT_VER:-6149}"

# Anything that is not a stock 4513 build is "custom": it cannot match the
# reference SHA1, and it must not overwrite out/kitsune.bin, which is the
# committed reference every other check here compares against. Both consequences
# follow from this one flag rather than being decided separately and drifting.
CUSTOM=""
if [ "$KIT_VER" != "6149" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_DEV_DOMAIN:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_PROD_DOMAIN:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_HTTP_EXPORT_FIX:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_ENABLE_SERVERS:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_KEYWORD_MODEL:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_SPEECH_GAIN_FIX:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_NET_RESUME_FIX:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_FEATURE_UPLOAD:-}" ]; then CUSTOM=1; fi
if [ -n "${KITSUNE_DEV_DOMAIN:-}" ] && [ -n "${KITSUNE_PROD_DOMAIN:-}" ]; then
  echo "set one of KITSUNE_DEV_DOMAIN or KITSUNE_PROD_DOMAIN, not both" >&2; exit 1
fi

if [ -n "$CUSTOM" ]; then
  # "-" not ":-", so an empty value stays empty and means "do not compare".
  EXPECT_SHA="${EXPECT_SHA-}"
else
  EXPECT_SHA="${EXPECT_SHA-90453011b136916d3b67aa302e86b7526c074420}"
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
# The voice branch dials five hosts, not two. DATA and MESSEJI are each defined
# TWICE (an #ifdef USE_SHA2 branch and a non-SHA2 branch); the compiled branch
# depends on a build flag, so rewrite both. SPEECH and WS are defined once each.
# SPEECH is the wake-word upload (kitsune/hlo_audio_tools.c get_speech_server(),
# HTTPS POST /v2/upload/audio); WS is a websocket orb does not serve but the
# firmware still dials (kitsune/hlo_http.c get_ws_server()). Each pattern carries
# the exact match count it must find, so a structural change in endpoints.h fails
# loudly here rather than silently leaving a hello.is host in the image.
subs = [(r'(#define\s+%s_DATA_SERVER\s+)"[^"]*"' % slot,    r'\1"sense-in.%s"' % domain, 2),
        (r'(#define\s+%s_MESSEJI_SERVER\s+)"[^"]*"' % slot, r'\1"messeji.%s"' % domain, 2),
        (r'(#define\s+%s_SPEECH_SERVER\s+)"[^"]*"' % slot,  r'\1"speech.%s"' % domain, 1),
        (r'(#define\s+%s_WS_SERVER\s+)"[^"]*"' % slot,      r'\1"ws.%s"' % domain, 1)]
for pat, rep, want in subs:
    s, n = re.subn(pat, rep, s)
    assert n == want, "endpoints.h: expected %d matches for %s, got %d" % (want, pat, n)
# TEST_SERVER is a manual UART diagnostic (commands.c TestNetwork_RunTests),
# never dialed on its own and with no DEV/PROD split. Rewrite it too so the
# compiled image carries no hello.is host at all.
s, n = re.subn(r'(#define\s+TEST_SERVER\s+)"[^"]*"', r'\1"stress-in.%s"' % domain, s)
assert n == 1, "endpoints.h: expected 1 TEST_SERVER, got %d" % n
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

# 2c. Optional: firmware C fixes, kept as real git patches under patches/ and
# applied with `git apply` so they compile-check at author time and review as
# normal diffs. Each one makes the image differ from the byte-exact reference,
# so the SHA1 check is skipped like any other custom build.
#
# KITSUNE_HTTP_EXPORT_FIX fixes the `x` console command's HTTP file export
# (used to pull files, e.g. the SD-card sleep tones, off the device). The POST
# stream's close never flushed the final buffered chunk or the terminating
# 0-length chunk (the finalizer was #if 0'd out), so a file small enough to fit
# in one scratch buffer never completed its transfer. See
# patches/http-export-flush.patch (hello-sense github issue #1).
#
# (The CC3200 orb's OTA-reliability patch is fatfs_cmd.c boot-record surgery
# specific to that chip's flash layout; it does not apply to the CC3220SF and
# lives in kitsune-4513, not here.)
if [ -n "${KITSUNE_HTTP_EXPORT_FIX:-}" ]; then
  echo ">> applying patches/http-export-flush.patch"
  git -C "$WORK/src" apply --whitespace=nowarn "$HERE/patches/http-export-flush.patch"
  echo "   HTTP export flush fix applied"
fi

# KITSUNE_ENABLE_SERVERS turns on the on-device telnet console (port 224), which
# pipes what it receives into the command processor, so files can be pulled off
# the device over the network (telnet -> `x` export) with no UART. The console
# code is #ifdef BUILD_SERVERS / BUILD_TELNET_SERVER, off since production, and
# defining those also rips out the uart_logger + analytics and rewires logging
# to telnet. So instead of defining them, the patch force-includes ONLY the
# telnet/http server code (the guards -> #if 1) and fixes one SDK bit-rot
# (SlSockNonblocking_t.NonBlockingEnabled). Logging, radio-tx and analytics are
# left exactly as a normal build. Turn the servers back off (drop this flag) for
# a clean image once the files are recovered.
if [ -n "${KITSUNE_ENABLE_SERVERS:-}" ]; then
  echo ">> applying patches/enable-telnet-console.patch"
  git -C "$WORK/src" apply --whitespace=nowarn "$HERE/patches/enable-telnet-console.patch"
  echo "   on-device telnet console (port 224) enabled"
fi

# KITSUNE_SPEECH_GAIN_FIX lifts the mic level on the wake-word speech upload.
# The device streamed the raw mic to the server at 1-7% full scale, so
# speech-to-text saw noise; the wake detector was unaffected because it AGCs
# its own copy. This applies a saturating gain just before the ADPCM encoder,
# on the upload path only. See patches/speech-gain.patch.
if [ -n "${KITSUNE_SPEECH_GAIN_FIX:-}" ]; then
  echo ">> applying patches/speech-gain.patch"
  git -C "$WORK/src" apply --whitespace=nowarn "$HERE/patches/speech-gain.patch"
  echo "   speech upload gain applied"
fi

# KITSUNE_NET_RESUME_FIX re-arms the wake word at the start of every detection
# session. The voice path could leave the net paused (_is_net_running=0) after a
# command, and initialize did not reset it, so the device went deaf after a few
# interactions until a power cycle. See patches/keyword-net-resume.patch.
if [ -n "${KITSUNE_NET_RESUME_FIX:-}" ]; then
  echo ">> applying patches/keyword-net-resume.patch"
  git -C "$WORK/src" apply --whitespace=nowarn "$HERE/patches/keyword-net-resume.patch"
  # keyword-net-resume re-arms at initialize (fixes deaf-until-power-cycle);
  # voice-command-resume re-arms right after each voice command's stream loop,
  # which pauses the net on speech start and breaks out on every path WITHOUT a
  # resume. Without this second patch the device is deaf for ~a minute after
  # each command ("responds once, then ignores me, then works again").
  echo ">> applying patches/voice-command-resume.patch"
  git -C "$WORK/src" apply --whitespace=nowarn "$HERE/patches/voice-command-resume.patch"
  echo "   keyword-net resume fixes applied (initialize + post-command)"
fi

# KITSUNE_FEATURE_UPLOAD enables the on-detection upload of the wake net's own
# int8 feature window to /audio/keyword_features (real training data). Hello
# disabled it over an upload deadlock; the patch fixes that race (stops the audio
# task writing the circular buffer before the network task reads it) and
# uncomments the trigger. See patches/feature-upload-enable.patch.
if [ -n "${KITSUNE_FEATURE_UPLOAD:-}" ]; then
  echo ">> applying patches/feature-upload-enable.patch"
  git -C "$WORK/src" apply --whitespace=nowarn "$HERE/patches/feature-upload-enable.patch"
  echo "   keyword feature upload enabled"
fi


# KITSUNE_KEYWORD_MODEL swaps the compiled-in wake-word neural net. Point it at a
# tinytensor model_*.c (as produced by the heysense trainer's export_to_c.py); it
# is copied into the tensor dir and keyword_net.c's NEURAL_NET_MODEL #define is
# repointed at it. The net keeps the shipped 7-class shape, so class 1 (which the
# okay_sense callback reads) becomes the new keyword: a drop-in wake-word change,
# no other firmware edit.
if [ -n "${KITSUNE_KEYWORD_MODEL:-}" ]; then
  [ -f "$KITSUNE_KEYWORD_MODEL" ] || { echo "KITSUNE_KEYWORD_MODEL not found: $KITSUNE_KEYWORD_MODEL" >&2; exit 1; }
  echo ">> swapping wake-word model -> $(basename "$KITSUNE_KEYWORD_MODEL")"
  cp "$KITSUNE_KEYWORD_MODEL" "$WORK/src/kitsune/tensor/model_custom_keyword.c"
  sed -i.bak -E 's/^#define NEURAL_NET_MODEL .*/#define NEURAL_NET_MODEL "model_custom_keyword.c"/' \
    "$WORK/src/kitsune/tensor/keyword_net.c"
  rm -f "$WORK/src/kitsune/tensor/keyword_net.c.bak"
  echo "   NEURAL_NET_MODEL repointed at model_custom_keyword.c"
fi

# 3. Run the full build inside the container.
echo ">> building (headless CCS under $PLATFORM; ~30-90 min under emulation)..."
docker run --rm --platform "$PLATFORM" \
  -e KIT_VER="$KIT_VER" -e EXPECT_SHA="$EXPECT_SHA" \
  -e KITSUNE_FEATURE_UPLOAD="${KITSUNE_FEATURE_UPLOAD:-}" \
  -v "$WORK/src:/src" \
  -v "$HERE:/recipe:ro" \
  "$IMAGE" bash /recipe/build_in_container.sh

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
