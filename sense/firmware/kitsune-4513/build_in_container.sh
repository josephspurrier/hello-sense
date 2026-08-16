#!/bin/bash
# Runs INSIDE the linux/386 CCS container. Source is at /src (kitsune @ 1.9.2,
# submodules populated). Produces /src/kitsune/main/ccs/exe/kitsune.bin,
# byte-exact to build 4513.
#
# Build strategy: drive the CCS-generated makefiles directly with gmake. These
# makefiles ARE the CCS 6.0.1 build orchestration (the exact armcl/armar command
# lines projectBuild would run); vendored in generated_makefiles.tar.gz. This
# avoids the Eclipse "apps" (projectImport/projectBuild), whose 32-bit XPCOM
# runtime is unreliable and occasionally wedges under qemu emulation.
set -e

KIT_VER="${KIT_VER:-4513}"
EXPECT_SHA="${EXPECT_SHA:-0c5f639e1290df0e3a5f8641d670923ed71a5e63}"
CG=/root/ti/ccsv6/tools/compiler/ti-cgt-arm_5.1.5   # symlink -> /opt/cgt-5.1.5
GMAKE=make                                          # GNU Make 4.3 (== CCS's bundled gmake)
MAKEFILES_TGZ="${MAKEFILES_TGZ:-/root/generated_makefiles.tar.gz}"

echo "=== 0. stamp KIT_VER $KIT_VER ==="
printf '#ifndef KIT_VER\n#define KIT_VER %s\n#endif\n' "$KIT_VER" \
  > /src/kitsune/kitsune_version.h

echo "=== 1. lay down CCS-generated makefiles ==="
tar xzf "$MAKEFILES_TGZ" -C /src
# Build config dir per project (matches the makefiles' locations).
DRIVERLIB=/src/driverlib/ccs/Release
SIMPLELINK=/src/simplelink/ccs/OS
OSLIB=/src/oslib/ccs/free_rtos
FATFS=/src/third_party/fatfs/ccs/Release
KITSUNE=/src/kitsune/main/ccs/Release
for d in "$DRIVERLIB" "$SIMPLELINK" "$OSLIB" "$FATFS" "$KITSUNE"; do
  [ -f "$d/makefile" ] || { echo "missing makefile in $d"; exit 1; }
done
# Archive/link output dirs the makefiles write to but do not create themselves
# (CCS creates them at project-setup time; a bare gmake does not).
mkdir -p /src/simplelink/ccs/exe /src/oslib/ccs/exe /src/kitsune/main/ccs/exe

# gmake in a build dir; pre-create obj output subdirs the rules expect.
build(){ # $1=dir  $2=label
  local d="$1" label="$2"
  echo "  -- $label ($d)"
  ( cd "$d"
    # Some object targets live in subdirs (e.g. ./common/x.obj); create those.
    # Use dirname + if (not &&) so a bare "adc.obj" (dirname ".") neither becomes
    # a directory nor trips `set -e` via a false test as the loop's last status.
    awk -F'"' '/\.obj/{print $2}' subdir_vars.mk 2>/dev/null | sort -u \
      | while read -r o; do
          sub=$(dirname "$o")
          if [ "$sub" != "." ] && [ -n "$sub" ]; then mkdir -p "$sub"; fi
        done
    "$GMAKE" -j1 all )
}

echo "=== 2. build libraries then app (CI order) ==="
build "$DRIVERLIB"  driverlib
build "$SIMPLELINK" simplelink
build "$OSLIB"      oslib
build "$FATFS"      fatfs
build "$KITSUNE"    kitsune   # compiles all app objects (and links, in wrong
                              # order; that link is discarded by step 3)

echo "=== 2b. verify every library archive exists and is non-empty ==="
AR="$CG/bin/armar"
declare -A LIBS=(
  [fatfs.a]="$FATFS/fatfs.a"
  [FreeRTOS.a]="/src/oslib/ccs/exe/FreeRTOS.a"
  [simplelink.a]="/src/simplelink/ccs/exe/simplelink.a"
  [driverlib.a]="$DRIVERLIB/driverlib.a"
)
for name in "${!LIBS[@]}"; do
  a="${LIBS[$name]}"
  [ -s "$a" ] || { echo "MISSING/EMPTY LIBRARY: $name ($a)"; exit 1; }
  echo "  $name: $("$AR" t "$a" 2>/dev/null | grep -c '\.obj') objects"
done
# The tell-tale of missing submodules: oslib short of its 14 FreeRTOS objects.
NF=$("$AR" t /src/oslib/ccs/exe/FreeRTOS.a 2>/dev/null | grep -c '\.obj')
[ "$NF" -ge 14 ] || { echo "FreeRTOS.a has only $NF/14 objects (submodules not populated?)"; exit 1; }

echo "=== 3. reorder-relink in the reference module order ==="
bash /root/relink_reforder.sh

echo "=== 4. extract .bin (section concatenation) ==="
python3 /root/extract_bin.py \
  /src/kitsune/main/ccs/exe/kitsune_reord.out \
  /src/kitsune/main/ccs/exe/kitsune.bin

echo "=== 5. verify ==="
GOT=$(sha1sum /src/kitsune/main/ccs/exe/kitsune.bin | cut -d' ' -f1)
echo "  built : $GOT"
echo "  expect: $EXPECT_SHA"
if [ "$GOT" = "$EXPECT_SHA" ]; then
  echo "  RESULT: BYTE-EXACT MATCH"
else
  echo "  RESULT: MISMATCH"; exit 1
fi
