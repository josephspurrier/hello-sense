#!/bin/bash
# Runs INSIDE the linux/386 CCS container for the CC3220SF voice build.
# Source at /src (kitsune @ fab199b3, submodules populated). Recipe dir mounted
# at /recipe (holds voice_compile.inc, voice_relink_objs.inc, sdk_makefiles.tar.gz).
# Produces /src/kitsune/main/ccs/exe/kitsune.bin, target SHA1 90453011...
#
# Strategy: build the 4 SDK archives with the orb's CCS-generated makefiles
# (SDK source is shared), compile the 59 app objects directly (the object set the
# reference image actually linked, per pvt-kitsune.map, sources disambiguated via
# the CCS .project), then reorder-relink and extract. See BUILD_NOTES.md.
set -e
KIT_VER="${KIT_VER:-6149}"
EXPECT_SHA="${EXPECT_SHA-90453011b136916d3b67aa302e86b7526c074420}"
export CG=/root/ti/ccsv6/tools/compiler/ti-cgt-arm_5.1.5
GMAKE=make

# vfplib links need a vfplib run-time-support library. The linker's auto-build uses
# the STANDARD options (hardware float, Tag_ABI_VFP_args=1), which are ABI-incompatible
# with the soft-float (vfplib) app+SDK objects -> the linker then refuses to pull RTS
# objects (memcpy/memset/atoi/__aeabi_uidivmod/__TI_decompress_*), leaving them
# unresolved. So we build the RTS library ONCE with the exact vfplib options and install
# it into the CGT lib dir; the link then finds it and does no auto-build. mklib needs unzip.
command -v unzip >/dev/null 2>&1 || { echo "installing unzip for mklib..."; (apt-get update -qq && apt-get install -y -qq unzip) >/dev/null 2>&1 || echo "  WARN: could not install unzip"; }
# install to the mounted /src so it caches across --rm runs; relink adds -i/src/rtslib.
mkdir -p /src/rtslib
RTSLIB="/src/rtslib/rtsv7M4_T_le_eabi.lib"
if [ -s "$RTSLIB" ]; then
  echo "=== 0b. vfplib RTS lib already built, skipping ==="
else
  echo "=== 0b. build vfplib RTS lib (rtsv7M4_T_le_eabi.lib) ==="
  "$CG/lib/mklib" --index="$CG/lib/libc.a" --pattern=rtsv7M4_T_le_eabi.lib \
    --install_to="/src/rtslib" --compiler_bin_dir="$CG/bin" --name=rtsv7M4_T_le_eabi.lib \
    --options="-mv7M4 --code_state=16 --float_support=vfplib --abi=eabi -me --fp_mode=relaxed" 2>&1 | tail -2 || true
  [ -s "$RTSLIB" ] || { echo "FAILED to build $RTSLIB"; exit 1; }
  echo "  built $RTSLIB ($(stat -c %s "$RTSLIB") bytes)"
fi

echo "=== 0. stamp KIT_VER $KIT_VER ==="
printf '#ifndef KIT_VER\n#define KIT_VER %s\n#endif\n' "$KIT_VER" > /src/kitsune/kitsune_version.h

DA=/src/driverlib/ccs/Release/driverlib.a; SA=/src/simplelink/ccs/exe/simplelink.a
FA=/src/oslib/ccs/exe/FreeRTOS.a; TA=/src/third_party/fatfs/ccs/Release/fatfs.a
if [ -s "$DA" ] && [ -s "$SA" ] && [ -s "$FA" ] && [ -s "$TA" ]; then
  echo "=== 1. SDK archives already built, skipping (fast iterate) ==="
else
echo "=== 1. lay down SDK makefiles + build the 4 archives ==="
tar xzf /recipe/sdk_makefiles.tar.gz -C /src --warning=no-unknown-keyword
mkdir -p /src/simplelink/ccs/exe /src/oslib/ccs/exe /src/kitsune/main/ccs/exe /src/kitsune/main/ccs/Release
build(){ echo "  -- building $1"; ( cd "$1"; awk -F'"' '/\.obj/{print $2}' subdir_vars.mk 2>/dev/null | sort -u | while read -r o; do s=$(dirname "$o"); if [ "$s" != "." ] && [ -n "$s" ]; then mkdir -p "$s"; fi; done; "$GMAKE" -j1 all ); }
build /src/driverlib/ccs/Release
build /src/simplelink/ccs/OS
build /src/oslib/ccs/free_rtos
build /src/third_party/fatfs/ccs/Release
for a in /src/driverlib/ccs/Release/driverlib.a /src/simplelink/ccs/exe/simplelink.a /src/oslib/ccs/exe/FreeRTOS.a /src/third_party/fatfs/ccs/Release/fatfs.a; do
  [ -s "$a" ] || { echo "MISSING SDK ARCHIVE: $a"; exit 1; }
done
fi
mkdir -p /src/kitsune/main/ccs/exe /src/kitsune/main/ccs/Release

echo "=== 2. compile the 106 app objects directly ==="
cd /src/kitsune/main/ccs/Release
source /recipe/voice_compile.inc
# Enabling the keyword feature upload makes its trigger live, which pulls in two
# objects the stock image dead-code-eliminated (the SimpleMatrix proto and the
# upload helpers). Compile them only under the flag so the byte-exact stock build
# is untouched.
if [ -n "${KITSUNE_FEATURE_UPLOAD:-}" ]; then
  source /recipe/voice_feature_upload_compile.inc
fi
echo "  compiled $(find . -name '*.obj' | wc -l) app objects"

echo "=== 3. reorder-relink (voice reference order + CC3220sf.cmd + __SF_DEBUG__) ==="
bash /recipe/relink_reforder.sh

echo "=== 4. extract .bin ==="
python3 /recipe/extract_bin.py /src/kitsune/main/ccs/exe/kitsune_reord.out /src/kitsune/main/ccs/exe/kitsune.bin

echo "=== 5. verify ==="
GOT=$(sha1sum /src/kitsune/main/ccs/exe/kitsune.bin | cut -d' ' -f1)
SZ=$(stat -c %s /src/kitsune/main/ccs/exe/kitsune.bin)
echo "  built : $GOT ($SZ bytes)"
echo "  expect: $EXPECT_SHA (360984 bytes)"
[ "$GOT" = "$EXPECT_SHA" ] && echo "  RESULT: BYTE-EXACT MATCH" || echo "  RESULT: MISMATCH (expected on early iterations; diff kitsune_reord.out vs reference/pvt-kitsune.out)"
