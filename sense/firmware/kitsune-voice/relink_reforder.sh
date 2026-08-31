#!/bin/bash
# Link the 59 voice app objects + SDK archives for the CC3220SF. Voice app builds
# at -O1 (no -O4 whole-program pass), so no archive-reversal is needed; object
# order = the reference .text order (voice_relink_objs.inc). Refine order against
# reference/pvt-kitsune.out if placement drifts.
set -e
CG=/root/ti/ccsv6/tools/compiler/ti-cgt-arm_5.1.5
OBJS=$(cat /recipe/voice_relink_objs.inc)
cd /src/kitsune/main/ccs/Release

"$CG/bin/armcl" -mv7M4 --code_state=16 --float_support=vfplib --abi=eabi -me -O1 --opt_for_speed=0 --fp_mode=relaxed -g --gcc \
  --define=ccs --define=ARM_MATH_CM4 --define=__TI_ARM__ --define=FPM_DEFAULT --define=__SF_DEBUG__ --define=ARM --define=TARGET_IS_CC3200 --define=USE_FREERTOS --define=TI_CODEC \
  --display_error_number --diag_warning=225 --diag_wrap=off --gen_func_subsections=on --unaligned_access=off --ual \
  -z -m"kitsune_reord.map" --heap_size=0x200 --stack_size=0x600 -i"/src/rtslib" -i"$CG/lib" -i"$CG/include" --reread_libs --warn_sections \
  --xml_link_info="kitsune_reord_linkInfo.xml" --rom_model --trampolines=on --minimize_trampolines=postorder --unused_section_elimination=on --compress_dwarf=on \
  -o "/src/kitsune/main/ccs/exe/kitsune_reord.out" \
  $OBJS \
  "../CC3220sf.cmd" \
  -l"/src/third_party/fatfs/ccs/Release/fatfs.a" -l"/src/oslib/ccs/exe/FreeRTOS.a" -l"/src/simplelink/ccs/exe/simplelink.a" -l"/src/driverlib/ccs/Release/driverlib.a" \
  -l"/src/rtslib/rtsv7M4_T_le_eabi.lib"

echo "== linked =="; ls -la /src/kitsune/main/ccs/exe/kitsune_reord.out
